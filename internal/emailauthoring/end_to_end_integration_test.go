//go:build integration

package emailauthoring_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// endToEnd is the Email Authoring use cases as the server wires them, over
// a database and SpiceDB.
type endToEnd struct {
	db       *gorm.DB
	ctx      context.Context
	spiceDB  *auth.SpiceDBClient
	ports    emailauthoringpostgres.Ports
	service  *emailauthoring.Service
	internal *emailauthoring.InternalService
}

func newEndToEnd(t *testing.T, db *gorm.DB) endToEnd {
	t.Helper()
	ctx, spiceDB := testutil.IntegrationAdminAuditContext(t, db)
	runtime := emailauthoringadapter.NewRuntime()
	ports := emailauthoringpostgres.Ports{
		ContentBlocks: testutil.NewEmailContentBlockStore(t, spiceDB), References: emailauthoringadapter.NewCampaignDeliveryReferences(),
		Members: memberadapter.ContributorMembers{}, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), Rendering: runtime,
	}
	units, err := emailauthoringpostgres.NewUnits(db, ports)
	require.NoError(t, err)
	audit := apitelemetry.NewDurableWriter(db)
	service, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
		TestEmails: &testEmailQueue{}, Locales: runtime,
	})
	require.NoError(t, err)
	internal, err := emailauthoring.NewInternalService(emailauthoring.InternalDeps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return endToEnd{db: db, ctx: ctx, spiceDB: spiceDB, ports: ports, service: service, internal: internal}
}

func uniqueKey(prefix string) string {
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// lockRow holds a FOR UPDATE lock on one row until the test releases it.
func lockRow(t *testing.T, db *gorm.DB, table, id string) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	var locked string
	require.NoError(t, tx.Raw("SELECT id::text FROM "+table+" WHERE id = ? FOR UPDATE", id).Scan(&locked).Error)
	require.Equal(t, id, locked)
	return tx
}

// An administrator demoted while the template update waited on the root lock
// is denied once the lock is released: the fresh check runs after the lock.
func TestEmailTemplateUpdateRechecksAuthorityAfterWaitingOnItsRootIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t, testutil.NewConcurrentPostIntegrationDB(t))
	created, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("fence"), Name: "Original template", Subject: "Subject", SourceLocale: "en",
	})
	require.NoError(t, err)

	lockTx := lockRow(t, e.db, "email_template", created.ID)
	result := make(chan error, 1)
	go func() {
		name := "must not persist"
		_, err := e.service.UpdateEmailTemplate(e.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: created.ID, Name: &name})
		result <- err
	}()
	testutil.RequireLockWaiters(t, e.db, 1)
	testutil.GrantIntegrationGlobalRole(t, e.spiceDB, auth.GetUser(e.ctx).IdentityID.String(), policyv1.Role.User())
	require.NoError(t, lockTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, codeOf(<-result))
	var name string
	require.NoError(t, e.db.Table("email_template").Select("name").Where("id = ?", created.ID).Scan(&name).Error)
	require.Equal(t, "Original template", name)
}

// A layout deletion and a template assignment of the layout both lock the
// layout first, so whichever wins, neither deadlocks and the other answers
// the state the winner left.
func TestEmailLayoutDeleteAndTemplateAssignmentDoNotDeadlockIntegration(t *testing.T) {
	t.Parallel()
	e := newEndToEnd(t, testutil.NewConcurrentPostIntegrationDB(t))
	layout, err := e.service.CreateEmailLayout(e.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Race layout", Key: uniqueKey("race_layout"), HTMLContent: "<html><body>{{content}}</body></html>", SourceLocale: "en",
	})
	require.NoError(t, err)
	template, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("race_template"), Name: "Race template", Subject: "Race", SourceLocale: "en",
	})
	require.NoError(t, err)

	lockTx := lockRow(t, e.db, "email_layout", layout.ID)
	deleted, assigned := make(chan error, 1), make(chan error, 1)
	go func() {
		deleted <- e.service.DeleteEmailLayout(e.ctx, emailauthoring.DeleteEmailLayoutCommand{ID: layout.ID})
	}()
	go func() {
		_, err := e.service.UpdateEmailTemplate(e.ctx, emailauthoring.UpdateEmailTemplateCommand{ID: template.ID, LayoutID: &layout.ID})
		assigned <- err
	}()
	testutil.RequireLockWaiters(t, e.db, 2)
	require.NoError(t, lockTx.Rollback().Error)
	deleteErr, assignErr := <-deleted, <-assigned

	var stored model.EmailTemplate
	require.NoError(t, e.db.First(&stored, "id = ?", template.ID).Error)
	if deleteErr == nil {
		require.Equal(t, connect.CodeNotFound, codeOf(assignErr))
		require.Nil(t, stored.LayoutID)
		return
	}
	require.NoError(t, assignErr)
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(deleteErr))
	require.Equal(t, layout.ID, *stored.LayoutID)
}

// Collaboration, interchange and a provider delivery on the real content
// store share one document revision: a target write keeps it, a stale
// interchange loses to the newer target, the provider's sparse overlay
// falls back to the source, and a source edit refreshes the target
// projection and the custom variables.
func TestEmailTemplateTargetsKeepTheSharedRevisionAcrossWritersIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupOryStackWithLocalKratos(t).DB
	e := newEndToEnd(t, db)
	created, err := e.service.CreateEmailTemplate(e.ctx, emailauthoring.CreateEmailTemplateCommand{
		Key: uniqueKey("exact_target"), Name: "Exact target Template", Subject: "Source subject", SourceLocale: "en",
	})
	require.NoError(t, err)
	contributorID := testutil.IntegrationUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, e.spiceDB, contributorID)
	collab := testutil.NewAuditContext(t, auth.GetUser(e.ctx).IdentityID.String(), contributorID)

	sourceWrite, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "en",
		Batch: testutil.NewParagraphBatch(created.Projection.Document, created.Projection.Revision, "en",
			"Source body {{ Recipient_Name }} {{custom_code}} {{recipient_name}}", []string{contributorID}),
	})
	require.NoError(t, err)
	var variables model.EmailTemplate
	require.NoError(t, db.Select("variables").First(&variables, "id = ?", created.ID).Error)
	require.Equal(t, []string{"custom_code", "recipient_name"}, []string{variables.Variables[0].Name, variables.Variables[1].Name})

	store := emailauthoringpostgres.NewStore(db, e.ports)
	absent := false
	interchangeSubject := "대상 제목"
	createdTarget, err := emailauthoring.ApplyTemplateTranslationInterchange(collab, store, created.ID, "en", emailcontent.InterchangeMutation{
		TargetLocale: "ko", ExpectedDocumentRevision: sourceWrite.DocumentRevision, ExpectedPresence: absent,
		Subject: &interchangeSubject, ContributorMemberID: contributorID, Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.True(t, createdTarget.Changed)
	source, err := emailauthoring.LoadTemplateTranslationSourceDocument(collab, store, created.ID)
	require.NoError(t, err)
	require.Equal(t, sourceWrite.DocumentRevision, source.ContentDocumentRevision)
	blockID := source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetBlockId()

	targetRevision := createdTarget.Revision
	targetWrite, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "ko", ExpectedTargetRevision: &targetRevision,
		Batch: paragraphBatch(uuid.MustParse(sourceWrite.DocumentRevision), "ko", blockID, "번역 본문", false, contributorID),
	})
	require.NoError(t, err)
	require.Equal(t, sourceWrite.DocumentRevision, targetWrite.DocumentRevision)

	stale := "must not win"
	_, err = emailauthoring.ApplyTemplateTranslationInterchange(collab, store, created.ID, "en", emailcontent.InterchangeMutation{
		TargetLocale: "ko", ExpectedDocumentRevision: sourceWrite.DocumentRevision, ExpectedTargetRevision: &targetRevision,
		ExpectedPresence: true, Subject: &stale, ContributorMemberID: contributorID, Now: time.Now().UTC(),
	})
	var targetConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &targetConflict)

	providerSubject := "Provider subject"
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return emailauthoring.ApplyTemplateTranslationCandidate(collab,
			emailauthoringpostgres.Bind(tx, e.ports, apitelemetry.NewDurableWriter(db)),
			emailcontent.TranslationJob{
				EntityType: "email_template", EntityID: created.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: contributorID,
			},
			&translation.Candidate{
				Title: &providerSubject, ContentDocumentRevision: sourceWrite.DocumentRevision,
				ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}, ContentBlockLocaleDeletes: []string{blockID},
			},
			translation.EntryWrite{Title: &providerSubject, Now: time.Now().UTC()},
		)
	}))
	var audit struct {
		ActorKind     string `gorm:"column:actor_kind"`
		ActorMemberID string `gorm:"column:actor_member_id"`
	}
	require.NoError(t, db.Raw(`
		SELECT actor_kind, actor_member_id::text AS actor_member_id
		FROM domain_audit
		WHERE action = ? AND target_type = 'email_template' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"],"locale":"ko"}'::jsonb
		ORDER BY occurred_at DESC, audit_id DESC
		LIMIT 1`, sharedtelemetry.AuditEmailTemplateUpdated, created.ID).Scan(&audit).Error)
	require.Equal(t, "member", audit.ActorKind)
	require.Equal(t, contributorID, audit.ActorMemberID)
	var projection string
	require.NoError(t, db.Table("email_template_translation").Select("content_html").
		Where("entity_id = ? AND locale = 'ko'", created.ID).Scan(&projection).Error)
	require.Contains(t, projection, "Source body")

	updated, err := e.internal.ApplyTemplateBlockBatch(collab, emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: created.ID, Locale: "en",
		Batch: paragraphBatch(uuid.MustParse(sourceWrite.DocumentRevision), "en", blockID, "Updated source body", false, contributorID),
	})
	require.NoError(t, err)
	require.NotEqual(t, sourceWrite.DocumentRevision, updated.DocumentRevision)
	require.NoError(t, db.Table("email_template_translation").Select("content_html").
		Where("entity_id = ? AND locale = 'ko'", created.ID).Scan(&projection).Error)
	require.Contains(t, projection, "Updated source body")
}
