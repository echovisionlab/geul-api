//go:build integration

package public

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestSubmitRevalidatesMutableAccessPolicyAfterFormLock(t *testing.T) {
	db := testutil.NewIntegrationDB(t)

	tests := []struct {
		name     string
		mutation func(t *testing.T) structured.Fields
		wantCode connect.Code
	}{
		{
			name: "unpublished",
			mutation: func(_ *testing.T) structured.Fields {
				return structured.Fields{"status": managev1.FormStatus_FORM_STATUS_DRAFT.String()}
			},
			wantCode: connect.CodeFailedPrecondition,
		},
		{
			name: "opening moved to the future",
			mutation: func(_ *testing.T) structured.Fields {
				return structured.Fields{"opens_at": time.Now().Add(time.Hour)}
			},
			wantCode: connect.CodeFailedPrecondition,
		},
		{
			name: "closing moved to the past",
			mutation: func(_ *testing.T) structured.Fields {
				return structured.Fields{"closes_at": time.Now().Add(-time.Hour)}
			},
			wantCode: connect.CodeFailedPrecondition,
		},
		{
			name: "password added",
			mutation: func(t *testing.T) structured.Fields {
				hash, err := testFormPasswordHasher().Hash("new-password")
				require.NoError(t, err)
				return structured.Fields{"access_password": hash}
			},
			wantCode: connect.CodePermissionDenied,
		},
		{
			name: "authentication required",
			mutation: func(_ *testing.T) structured.Fields {
				return structured.Fields{"require_auth": true}
			},
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name: "role restriction added",
			mutation: func(_ *testing.T) structured.Fields {
				return structured.Fields{"allowed_roles": pq.StringArray{"ADMIN"}}
			},
			wantCode: connect.CodeUnauthenticated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			formID := uuid.NewString()
			documentID := uuid.NewString()
			require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?, 'compact')", documentID).Error)
			require.NoError(t, db.Create(&model.Form{
				ID:                formID,
				ContentDocumentID: documentID,
				SourceLocale:      "en",
				Status:            model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()),
				IsPublic:          true,
				CreatedAt:         time.Now().UTC(),
			}).Error)
			t.Cleanup(func() {
				require.NoError(t, db.Delete(&model.Form{}, "id = ?", formID).Error)
				require.NoError(t, db.Exec("DELETE FROM content_document WHERE id = ?", documentID).Error)
			})

			holder := db.Begin()
			require.NoError(t, holder.Error)
			holderCommitted := false
			t.Cleanup(func() {
				if !holderCommitted {
					_ = holder.Rollback().Error
				}
			})
			var locked model.Form
			require.NoError(t, holder.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&locked, "id = ?", formID).Error)
			require.NoError(t, holder.Model(&model.Form{}).
				Where("id = ?", formID).
				Updates(tc.mutation(t)).Error)

			lockAttempted := make(chan struct{})
			callbackName := "test:form-submit-lock:" + uuid.NewString()
			var signalOnce sync.Once
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx == nil || tx.Statement == nil || tx.Statement.Table != "form" {
					return
				}
				if _, ok := tx.Statement.Clauses["FOR"]; !ok {
					return
				}
				signalOnce.Do(func() { close(lockAttempted) })
			}))
			t.Cleanup(func() {
				require.NoError(t, db.Callback().Query().Remove(callbackName))
			})

			result := make(chan error, 1)
			go func() {
				_, submitErr := newPublicFormSubmissionService(t, db).Submit(
					context.Background(),
					connect.NewRequest(&openv1.SubmitFormRequest{
						FormId: formID,
						Data:   []byte(`{"answer":"value"}`),
					}),
				)
				result <- submitErr
			}()

			select {
			case <-lockAttempted:
			case <-time.After(5 * time.Second):
				require.FailNow(t, "submission did not attempt the authoritative form lock")
			}
			require.NoError(t, holder.Commit().Error)
			holderCommitted = true

			var submitErr error
			select {
			case submitErr = <-result:
			case <-time.After(5 * time.Second):
				require.FailNow(t, "submission did not finish after the form lock was released")
			}
			require.Error(t, submitErr)
			require.Equal(t, tc.wantCode, connect.CodeOf(submitErr))

			var count int64
			require.NoError(t, db.Model(&model.FormSubmission{}).
				Where("form_id = ?", formID).
				Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestSubmitDomainAuditAnonymousAndMemberIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	formID := uuid.NewString()
	documentID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?, 'compact')", documentID).Error)
	require.NoError(t, db.Create(&model.Form{
		ID: formID, ContentDocumentID: documentID, SourceLocale: "en",
		Status: model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()), IsPublic: true, CreatedAt: now,
	}).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Delete(&model.Form{}, "id = ?", formID).Error)
		require.NoError(t, db.Exec("DELETE FROM content_document WHERE id = ?", documentID).Error)
	})
	require.NoError(t, db.Exec(`INSERT INTO form_translation (entity_id, locale, title, content_json, created_at, updated_at) VALUES (?::uuid, 'en', 'Audit form', ?::jsonb, ?, ?)`, formID, `{"id":"audit-form","steps":[]}`, now, now).Error)
	service := newAuditedPublicFormSubmissionService(t, db, apitelemetry.NewDurableWriter(db))
	requestID := uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(requestID, sharedtelemetry.AnonymousActor{})
	require.NoError(t, err)
	_, err = service.Submit(sharedtelemetry.WithRequestContext(t.Context(), request), connect.NewRequest(&openv1.SubmitFormRequest{FormId: formID, Data: []byte(`{}`)}))
	require.NoError(t, err)

	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: "Audit submission member"})
	memberID := seedFormPublicMemberIdentityLink(t, db, identityID, "Audit submission member")
	memberSessionID := uuid.NewString()
	memberRequest, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID,
		MemberID:   memberID,
		SessionID:  memberSessionID,
	})
	require.NoError(t, err)
	memberCtx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), memberRequest), &auth.UserInfo{
		MemberID:      auth.MemberID(memberID),
		IdentityID:    auth.IdentityID(identityID),
		SessionID:     auth.SessionID(memberSessionID),
		Authenticated: true,
	})
	_, err = service.Submit(memberCtx, connect.NewRequest(&openv1.SubmitFormRequest{FormId: formID, Data: []byte(`{}`)}))
	require.NoError(t, err)

	var rows []struct {
		ActorKind, ActorMemberID, ParentID, RequestID string
		Attributes                                    []byte
	}
	require.NoError(t, db.Raw(`SELECT actor_kind, COALESCE(actor_member_id::text, '') AS actor_member_id, attributes->>'parent_id' AS parent_id, request_id::text AS request_id, attributes FROM domain_audit WHERE action = 'form_submission.created' AND target_type = 'form_submission' ORDER BY occurred_at, audit_id`).Scan(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, "anonymous", rows[0].ActorKind)
	require.Equal(t, formID, rows[0].ParentID)
	require.Equal(t, requestID, rows[0].RequestID)
	require.Equal(t, "member", rows[1].ActorKind)
	require.Equal(t, memberID, rows[1].ActorMemberID)
	for _, row := range rows {
		require.NotContains(t, string(row.Attributes), "IPAddress")
		require.NotContains(t, string(row.Attributes), "email")
		require.NotContains(t, string(row.Attributes), "answer")
	}
	failing := newAuditedPublicFormSubmissionService(t, db, failingFormSubmissionAuditAppender{})
	_, err = failing.Submit(sharedtelemetry.WithRequestContext(t.Context(), request), connect.NewRequest(&openv1.SubmitFormRequest{FormId: formID, Data: []byte(`{}`)}))
	require.Error(t, err)
	var submissionCount int64
	require.NoError(t, db.Model(&model.FormSubmission{}).Where("form_id = ?", formID).Count(&submissionCount).Error)
	require.EqualValues(t, 2, submissionCount)
}

type failingFormSubmissionAuditAppender struct{}

func (failingFormSubmissionAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("form submission audit unavailable")
}

func TestSubmitFailsClosedWhenDuplicateLookupFails(t *testing.T) {
	db := testutil.NewIntegrationDB(t)

	allowDuplicate := false
	formID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?, 'compact')", documentID).Error)
	require.NoError(t, db.Create(&model.Form{
		ID:                       formID,
		ContentDocumentID:        documentID,
		SourceLocale:             "en",
		Status:                   model.FormStatus(managev1.FormStatus_FORM_STATUS_PUBLISHED.String()),
		IsPublic:                 true,
		AllowDuplicateSubmission: &allowDuplicate,
		CreatedAt:                time.Now().UTC(),
	}).Error)
	now := time.Now().UTC()
	const sourceSchema = `{"id":"duplicate-lookup-schema","steps":[{"id":"step-1","fields":[{"id":"field-answer","key":"answer","type":"text"}]}]}`
	require.NoError(t, db.Exec(`
		INSERT INTO form_translation (
			entity_id, locale, title, content_json, created_at, updated_at
		) VALUES (?::uuid, 'en', 'Duplicate lookup form', ?::jsonb, ?, ?)
	`, formID, sourceSchema, now, now).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Delete(&model.Form{}, "id = ?", formID).Error)
	})

	injectedErr := errors.New("injected duplicate lookup failure")
	callbackName := "test:form-submit-duplicate-lookup:" + uuid.NewString()
	var injectOnce sync.Once
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx != nil && tx.Statement != nil && tx.Statement.Table == "form_submission" {
			injectOnce.Do(func() { tx.AddError(injectedErr) })
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Query().Remove(callbackName))
	})

	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Name: "Duplicate Lookup Member",
	})
	memberID := seedFormPublicMemberIdentityLink(t, db, identityID, "Duplicate Lookup Member")
	t.Cleanup(func() {
		require.NoError(t, db.Delete(&model.Member{}, "id = ?", memberID).Error)
		require.NoError(t, db.Exec("DELETE FROM account_identity WHERE id = ?::uuid", identityID).Error)
		require.NoError(t, db.Exec("DELETE FROM kratos.identity_verifiable_addresses WHERE identity_id = ?::uuid", identityID).Error)
		require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", identityID).Error)
	})
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		MemberID:      auth.MemberID(memberID),
		IdentityID:    auth.IdentityID(identityID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
	_, err := newPublicFormSubmissionService(t, db).Submit(
		ctx,
		connect.NewRequest(&openv1.SubmitFormRequest{
			FormId: formID,
			Data:   []byte(`{"answer":"value"}`),
		}),
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.ErrorContains(t, err, injectedErr.Error())

	var count int64
	require.NoError(t, db.Model(&model.FormSubmission{}).Where("form_id = ?", formID).Count(&count).Error)
	require.Zero(t, count)
}

func testFormPasswordHasher() *crypto.PasswordHasher {
	return crypto.NewPasswordHasher(&crypto.Argon2idParams{
		Memory:      64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	})
}

// These constructors keep the legacy public integration package exercising
// the migrated Form boundary until the remaining public integration suite is
// split by domain.
func newPublicFormSubmissionService(t *testing.T, db *gorm.DB) *FormService {
	t.Helper()
	assets := formadapter.NewAssets("https://cdn.example.com")
	return NewFormService(db, testFormPasswordHasher(), testutil.IntegrationSpiceDB(t), formdomain.Dependencies{
		PublicAssets: assets,
	})
}

func newAuditedPublicFormSubmissionService(t *testing.T, db *gorm.DB, audit domainaudit.Appender) *FormService {
	t.Helper()
	assets := formadapter.NewAssets("https://cdn.example.com")
	return NewAuditedFormService(db, testFormPasswordHasher(), testutil.IntegrationSpiceDB(t), audit, formdomain.Dependencies{
		PublicAssets: assets,
	})
}
