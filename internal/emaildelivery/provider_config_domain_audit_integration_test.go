//go:build integration

package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type mailAdapterAuditRow struct {
	Action     string
	TargetType string `gorm:"column:target_type"`
	TargetID   string `gorm:"column:target_id"`
	Attributes []byte `gorm:"column:attributes"`
}

func TestMailAdapterProviderConfigDomainAuditIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })

	t.Run("exact changes and semantic no-op", func(t *testing.T) {
		prepareMailAdapterIntegrationCase(t, stack)
		testMailAdapterUpdatesAuditExactChangesAndSkipSemanticNoOps(
			t, stack.Postgres.DB, stack.SpiceDBClient,
		)
	})
	t.Run("audit failure rolls back mutation", func(t *testing.T) {
		prepareMailAdapterIntegrationCase(t, stack)
		testMailAdapterAuditFailureRollsBackMutation(
			t, stack.Postgres.DB, stack.SpiceDBClient,
		)
	})
}

func testMailAdapterUpdatesAuditExactChangesAndSkipSemanticNoOps(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) {
	admin := seedMailAdapterAuditAdmin(t, db)
	ctx := mailAdapterAuditedMemberContext(t, admin.IdentityID, admin.MemberID)
	grantMailAdapterIntegrationAdmin(t, spiceDB, admin.IdentityID)

	mailService := NewAuditedMailAdapterService(
		db, nil, nil, apitelemetry.NewDurableWriter(db), spiceDB,
	)
	createdMail, err := mailService.Create(ctx, connect.NewRequest(&managev1.CreateMailAdapterRequest{
		Name: "Created audited mail adapter", Type: managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES,
		Config: &managev1.CreateMailAdapterRequest_SesConfig{SesConfig: &managev1.SESConfig{
			Region: "ap-northeast-2", AccessKeyId: "created-mail-key",
			SecretAccessKey: "created-mail-secret", FromEmail: "created-mail@example.test",
		}},
	}))
	require.NoError(t, err)

	mail := insertedAuditedMailAdapter(t, db)
	mailName := "Audited mail adapter"
	mailActive := true
	mailPriority := int32(17)
	_, err = mailService.Update(ctx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
		Id: mail.ID, Name: &mailName, IsActive: &mailActive, Priority: &mailPriority,
	}))
	require.NoError(t, err)
	updatedAt := mailAdapterUpdatedAt(t, db, mail.ID)
	_, err = mailService.Update(ctx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
		Id: mail.ID, Name: &mailName, IsActive: &mailActive, Priority: &mailPriority,
	}))
	require.NoError(t, err)
	require.Equal(t, updatedAt, mailAdapterUpdatedAt(t, db, mail.ID))

	_, err = mailService.Delete(ctx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{
		Id: createdMail.Msg.Adapter.Id,
	}))
	require.NoError(t, err)
	_, err = mailService.Delete(ctx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{
		Id: createdMail.Msg.Adapter.Id,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	var rows []mailAdapterAuditRow
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, attributes
		FROM public.domain_audit
		WHERE action LIKE 'mail_adapter.%'
		ORDER BY action
	`).Scan(&rows).Error)
	require.Len(t, rows, 3)
	byAction := make(map[string]mailAdapterAuditRow, len(rows))
	for _, row := range rows {
		byAction[row.Action] = row
		require.NotContains(t, string(row.Attributes), "mail-audit-secret")
		require.NotContains(t, string(row.Attributes), "created-mail-secret")
		require.NotContains(t, string(row.Attributes), "mail-audit@example.test")
		require.NotContains(t, string(row.Attributes), "created-mail@example.test")
	}
	assertMailAdapterAudit(t, byAction[string(sharedtelemetry.AuditMailAdapterCreated)], sharedtelemetry.AuditMailAdapterCreated, createdMail.Msg.Adapter.Id, nil)
	assertMailAdapterAudit(t, byAction[string(sharedtelemetry.AuditMailAdapterUpdated)], sharedtelemetry.AuditMailAdapterUpdated, mail.ID, []string{"active", "name", "priority"})
	assertMailAdapterAudit(t, byAction[string(sharedtelemetry.AuditMailAdapterDeleted)], sharedtelemetry.AuditMailAdapterDeleted, createdMail.Msg.Adapter.Id, nil)
}

func testMailAdapterAuditFailureRollsBackMutation(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) {
	admin := seedMailAdapterAuditAdmin(t, db)
	ctx := mailAdapterAuditedMemberContext(t, admin.IdentityID, admin.MemberID)
	grantMailAdapterIntegrationAdmin(t, spiceDB, admin.IdentityID)
	mail := insertedAuditedMailAdapter(t, db)
	failing := NewAuditedMailAdapterService(db, nil, nil, failingMailAdapterAuditAppender{}, spiceDB)

	_, err := failing.Create(ctx, connect.NewRequest(&managev1.CreateMailAdapterRequest{
		Name: "failed audit mail create", Type: managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES,
		Config: &managev1.CreateMailAdapterRequest_SesConfig{SesConfig: &managev1.SESConfig{
			Region: "ap-northeast-2", AccessKeyId: "failed-mail-key",
			SecretAccessKey: "failed-mail-secret", FromEmail: "failed-mail@example.test",
		}},
	}))
	require.Error(t, err)
	var createCount int64
	require.NoError(t, db.Table("public.mail_adapter").
		Where("name = ?", "failed audit mail create").Count(&createCount).Error)
	require.Zero(t, createCount)

	mailName := "must not persist"
	_, err = failing.Update(ctx, connect.NewRequest(&managev1.UpdateMailAdapterRequest{
		Id: mail.ID, Name: &mailName,
	}))
	require.Error(t, err)
	var storedName string
	require.NoError(t, db.Table("public.mail_adapter").Select("name").
		Where("id = ?", mail.ID).Scan(&storedName).Error)
	require.Equal(t, mail.Name, storedName)

	_, err = failing.Delete(ctx, connect.NewRequest(&managev1.DeleteMailAdapterRequest{Id: mail.ID}))
	require.Error(t, err)
	var retainedCount int64
	require.NoError(t, db.Table("public.mail_adapter").Where("id = ?", mail.ID).
		Count(&retainedCount).Error)
	require.EqualValues(t, 1, retainedCount)

	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action LIKE ?", "mail_adapter.%").Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

func prepareMailAdapterIntegrationCase(
	t *testing.T,
	stack *testutil.BackendIntegrationStack,
) {
	t.Helper()
	require.NoError(t, testutil.ResetBackendIntegrationState(t.Context(), stack))
	t.Cleanup(func() {
		require.NoError(t, testutil.ResetBackendIntegrationState(
			context.Background(), stack,
		))
	})
}

type mailAdapterAuditAdmin struct {
	IdentityID string
	MemberID   string
}

func seedMailAdapterAuditAdmin(t *testing.T, db *gorm.DB) mailAdapterAuditAdmin {
	t.Helper()
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	email := "mail-adapter-audit-" + memberID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
			memberID, identityID,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO public.account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING`, identityID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO public.member (
				id, account_identity_id, nickname, onboarded, primary_email, available_emails
			)
			VALUES (?::uuid, ?::uuid, 'mail-adapter-audit-admin', true, ?, ARRAY[?]::text[])`,
			memberID, identityID, email, email,
		).Error
	}))
	return mailAdapterAuditAdmin{IdentityID: identityID, MemberID: memberID}
}

func grantMailAdapterIntegrationAdmin(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	identityID string,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
}

func mailAdapterAuditedMemberContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, &auth.UserInfo{
		SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(identityID),
		MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true,
	})
}

func insertedAuditedMailAdapter(t *testing.T, db *gorm.DB) model.MailAdapter {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	adapter := model.MailAdapter{
		Name:      "Original mail adapter",
		Type:      model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()),
		CreatedAt: now,
		UpdatedAt: &now,
	}
	require.NoError(t, adapter.SetConfig(&model.SESAdapterConfig{
		Region: "ap-northeast-2", AccessKeyID: "mail-audit-key",
		SecretAccessKey: "mail-audit-secret", FromEmail: "mail-audit@example.test",
	}))
	require.NoError(t, db.Create(&adapter).Error)
	return adapter
}

func mailAdapterUpdatedAt(t *testing.T, db *gorm.DB, id string) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, db.Table("public.mail_adapter").Select("updated_at").
		Where("id = ?", id).Scan(&updatedAt).Error)
	return updatedAt
}

func assertMailAdapterAudit(
	t *testing.T,
	row mailAdapterAuditRow,
	action sharedtelemetry.AuditAction,
	targetID string,
	changedFields []string,
) {
	t.Helper()
	require.Equal(t, string(action), row.Action)
	require.Equal(t, "mail_adapter", row.TargetType)
	require.Equal(t, targetID, row.TargetID)
	if len(changedFields) == 0 {
		require.JSONEq(t, `{}`, string(row.Attributes))
		return
	}
	expected, err := json.Marshal(struct {
		ChangedFields []string `json:"changed_fields"`
	}{ChangedFields: changedFields})
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(row.Attributes))
}

type failingMailAdapterAuditAppender struct{}

func (failingMailAdapterAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}
