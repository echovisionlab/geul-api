//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type translationProviderAuditRow struct {
	Action     string
	TargetType string `gorm:"column:target_type"`
	TargetID   string `gorm:"column:target_id"`
	Attributes []byte `gorm:"column:attributes"`
}

func TestTranslationProviderConfigDomainAuditIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })

	t.Run("exact changes and semantic no-op", func(t *testing.T) {
		prepareTranslationProviderIntegrationCase(t, stack)
		testTranslationProviderUpdatesAuditExactChangesAndSkipSemanticNoOps(
			t, stack.Postgres.DB, stack.SpiceDBClient,
		)
	})
	t.Run("audit failure rolls back mutation", func(t *testing.T) {
		prepareTranslationProviderIntegrationCase(t, stack)
		testTranslationProviderAuditFailureRollsBackMutation(
			t, stack.Postgres.DB, stack.SpiceDBClient,
		)
	})
	t.Run("concurrent source change uses locked latest row", func(t *testing.T) {
		prepareTranslationProviderIntegrationCase(t, stack)
		testTranslationProviderConfigUsesLockedLatestSource(
			t, stack.Postgres.DB, stack.SpiceDBClient,
		)
	})
}

func testTranslationProviderUpdatesAuditExactChangesAndSkipSemanticNoOps(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) {
	admin := seedTranslationProviderAuditAdmin(t, db)
	ctx := translationProviderAuditedMemberContext(t, admin)
	grantTranslationProviderIntegrationAdmin(t, spiceDB, admin)

	service := newAuditedTranslationProviderService(
		db, apitelemetry.NewDurableWriter(db), spiceDB,
	)
	provider := insertedAuditedTranslationProvider(t, db)
	created, err := service.CreateTranslationProvider(
		ctx,
		connect.NewRequest(&managev1.CreateTranslationProviderRequest{
			Name: "Created audited translation provider",
			Type: managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM,
			Config: &managev1.CreateTranslationProviderRequest_LlmConfig{
				LlmConfig: &managev1.LLMTranslationProviderConfig{
					ApiKey: "created-provider-secret",
					Preset: managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI,
				},
			},
		}),
	)
	require.NoError(t, err)

	providerName := "Audited translation provider"
	providerActive := true
	providerPriority := int32(23)
	providerModel := "translation-model-v2"
	request := &managev1.UpdateTranslationProviderRequest{
		Id: provider.ID, Name: &providerName, IsActive: &providerActive, Priority: &providerPriority,
		Config: &managev1.UpdateTranslationProviderRequest_LlmConfig{
			LlmConfig: &managev1.LLMTranslationProviderConfig{Model: providerModel},
		},
	}
	_, err = service.UpdateTranslationProvider(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	updatedAt := translationProviderUpdatedAt(t, db, provider.ID)
	_, err = service.UpdateTranslationProvider(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, updatedAt, translationProviderUpdatedAt(t, db, provider.ID))

	_, err = service.DeleteTranslationProvider(ctx, connect.NewRequest(
		&managev1.DeleteTranslationProviderRequest{Id: created.Msg.Provider.Id},
	))
	require.NoError(t, err)
	_, err = service.DeleteTranslationProvider(ctx, connect.NewRequest(
		&managev1.DeleteTranslationProviderRequest{Id: created.Msg.Provider.Id},
	))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	var rows []translationProviderAuditRow
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, attributes
		FROM public.domain_audit
		WHERE action LIKE 'translation_provider.%'
		ORDER BY action
	`).Scan(&rows).Error)
	require.Len(t, rows, 3)
	byAction := make(map[string]translationProviderAuditRow, len(rows))
	for _, row := range rows {
		byAction[row.Action] = row
		require.NotContains(t, string(row.Attributes), "provider-audit-secret")
		require.NotContains(t, string(row.Attributes), "created-provider-secret")
	}
	assertTranslationProviderAudit(t, byAction[string(sharedtelemetry.AuditTranslationProviderCreated)], sharedtelemetry.AuditTranslationProviderCreated, created.Msg.Provider.Id, nil)
	assertTranslationProviderAudit(t, byAction[string(sharedtelemetry.AuditTranslationProviderUpdated)], sharedtelemetry.AuditTranslationProviderUpdated, provider.ID, []string{"active", "config", "name", "priority"})
	assertTranslationProviderAudit(t, byAction[string(sharedtelemetry.AuditTranslationProviderDeleted)], sharedtelemetry.AuditTranslationProviderDeleted, created.Msg.Provider.Id, nil)
}

func testTranslationProviderAuditFailureRollsBackMutation(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) {
	admin := seedTranslationProviderAuditAdmin(t, db)
	ctx := translationProviderAuditedMemberContext(t, admin)
	grantTranslationProviderIntegrationAdmin(t, spiceDB, admin)
	provider := insertedAuditedTranslationProvider(t, db)
	failing := newAuditedTranslationProviderService(
		db, failingTranslationProviderAuditAppender{}, spiceDB,
	)

	_, err := failing.CreateTranslationProvider(
		ctx,
		connect.NewRequest(&managev1.CreateTranslationProviderRequest{
			Name: "failed audit provider create",
			Type: managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM,
			Config: &managev1.CreateTranslationProviderRequest_LlmConfig{
				LlmConfig: &managev1.LLMTranslationProviderConfig{
					ApiKey: "failed-provider-secret",
					Preset: managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_GEMINI,
				},
			},
		}),
	)
	require.Error(t, err)
	var createCount int64
	require.NoError(t, db.Table("public.translation_provider_config").
		Where("name = ?", "failed audit provider create").Count(&createCount).Error)
	require.Zero(t, createCount)

	providerName := "must not persist"
	_, err = failing.UpdateTranslationProvider(ctx, connect.NewRequest(
		&managev1.UpdateTranslationProviderRequest{Id: provider.ID, Name: &providerName},
	))
	require.Error(t, err)
	var storedName string
	require.NoError(t, db.Table("public.translation_provider_config").Select("name").
		Where("id = ?", provider.ID).Scan(&storedName).Error)
	require.Equal(t, provider.Name, storedName)

	_, err = failing.DeleteTranslationProvider(ctx, connect.NewRequest(
		&managev1.DeleteTranslationProviderRequest{Id: provider.ID},
	))
	require.Error(t, err)
	var retainedCount int64
	require.NoError(t, db.Table("public.translation_provider_config").
		Where("id = ?", provider.ID).Count(&retainedCount).Error)
	require.EqualValues(t, 1, retainedCount)

	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action LIKE ?", "translation_provider.%").Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

func testTranslationProviderConfigUsesLockedLatestSource(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) {
	admin := seedTranslationProviderAuditAdmin(t, db)
	grantTranslationProviderIntegrationAdmin(t, spiceDB, admin)
	provider := insertedAuditedTranslationProvider(t, db)
	service := newAuditedTranslationProviderService(
		db, apitelemetry.NewDurableWriter(db), spiceDB,
	)

	racingName := "test:translation-provider-preflight-source:" + uuid.NewString()
	var raced sync.Once
	var raceErr error
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(racingName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "translation_provider_config" {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		raced.Do(func() {
			winner := provider
			raceErr = winner.SetConfig(&model.LLMTranslationProviderConfig{
				APIKey: "race-winner-secret",
				Preset: model.TranslationLLMProviderPresetGemini,
				Model:  "race-winner-model",
			})
			if raceErr != nil {
				return
			}
			raceErr = db.Model(&model.TranslationProviderConfig{}).
				Where("id = ?", provider.ID).
				Updates(map[string]any{
					"config": winner.Config, "updated_at": time.Now().UTC(),
				}).Error
		})
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Query().Remove(racingName))
	})

	callerModel := "caller-model"
	_, err := service.UpdateTranslationProvider(
		translationProviderAuditedMemberContext(t, admin),
		connect.NewRequest(&managev1.UpdateTranslationProviderRequest{
			Id: provider.ID,
			Config: &managev1.UpdateTranslationProviderRequest_LlmConfig{
				LlmConfig: &managev1.LLMTranslationProviderConfig{Model: callerModel},
			},
		}),
	)
	require.NoError(t, err)
	require.NoError(t, raceErr)

	var stored model.TranslationProviderConfig
	require.NoError(t, db.First(&stored, "id = ?", provider.ID).Error)
	storedConfig, err := stored.GetLLMConfig()
	require.NoError(t, err)
	require.Equal(t, "race-winner-secret", storedConfig.APIKey)
	require.Equal(t, callerModel, storedConfig.Model)
	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action LIKE ?", "translation_provider.%").Count(&auditCount).Error)
	require.EqualValues(t, 1, auditCount)
}

func prepareTranslationProviderIntegrationCase(
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

type translationProviderAuditAdmin struct {
	IdentityID string
	MemberID   string
}

func seedTranslationProviderAuditAdmin(t *testing.T, db *gorm.DB) translationProviderAuditAdmin {
	t.Helper()
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	email := "translation-provider-audit-" + identityID + "@example.test"
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
			INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING`, identityID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO public.member (
				id, account_identity_id, nickname, onboarded, primary_email, available_emails
			) VALUES (
				?::uuid, ?::uuid, 'translation-provider-audit-admin', true, ?, ARRAY[?]::text[]
			)`, memberID, identityID, email, email,
		).Error
	}))
	return translationProviderAuditAdmin{IdentityID: identityID, MemberID: memberID}
}

func grantTranslationProviderIntegrationAdmin(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	admin translationProviderAuditAdmin,
) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(admin.IdentityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
}

func translationProviderAuditedMemberContext(t *testing.T, admin translationProviderAuditAdmin) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, &auth.UserInfo{
		SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(admin.IdentityID),
		MemberID: auth.MemberID(admin.MemberID), Authenticated: true, Onboarded: true,
	})
}

func newAuditedTranslationProviderService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *TranslationService {
	planner := og.NewPlanner(
		db, "", translationProviderOGRenderConfig{}, sitesettingsadapter.NewProjection(),
	)
	refresher := og.NewRefresher(
		planner, og.NewResolver(sitesettingsadapter.NewRequests()),
	)
	return NewAuditedTranslationService(
		db, &translationProviderPublisherStub{}, "", auditWriter, spiceDB, planner, refresher,
	)
}

type translationProviderOGRenderConfig struct{}

func (translationProviderOGRenderConfig) Snapshot(
	context.Context,
	*gorm.DB,
	string,
) ([]byte, string, error) {
	return []byte(`{}`), "translation-provider-test", nil
}

type translationProviderPublisherStub struct{}

func (*translationProviderPublisherStub) PublishTranslationGenerate(
	context.Context,
	*managev1.TranslationGenerateEvent,
) error {
	return nil
}

func (*translationProviderPublisherStub) PublishTranslationLifecycle(
	context.Context,
	*managev1.TranslationLifecycleEvent,
) error {
	return nil
}

func (*translationProviderPublisherStub) PublishContentUpdated(
	context.Context,
	*managev1.ContentUpdatedEvent,
) error {
	return nil
}

func insertedAuditedTranslationProvider(
	t *testing.T,
	db *gorm.DB,
) model.TranslationProviderConfig {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	provider := model.TranslationProviderConfig{
		Name:      "Original translation provider",
		Type:      model.TranslationProviderTypeLLM,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, provider.SetConfig(&model.LLMTranslationProviderConfig{
		APIKey: "provider-audit-secret",
		Preset: model.TranslationLLMProviderPresetGemini,
		Model:  "translation-model-v1",
	}))
	require.NoError(t, db.Create(&provider).Error)
	return provider
}

func translationProviderUpdatedAt(t *testing.T, db *gorm.DB, id string) time.Time {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, db.Table("public.translation_provider_config").Select("updated_at").
		Where("id = ?", id).Scan(&updatedAt).Error)
	return updatedAt
}

func assertTranslationProviderAudit(
	t *testing.T,
	row translationProviderAuditRow,
	action sharedtelemetry.AuditAction,
	targetID string,
	changedFields []string,
) {
	t.Helper()
	require.Equal(t, string(action), row.Action)
	require.Equal(t, "translation_provider", row.TargetType)
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

type failingTranslationProviderAuditAppender struct{}

func (failingTranslationProviderAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}
