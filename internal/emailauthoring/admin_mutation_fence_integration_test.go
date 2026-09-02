//go:build integration

package emailauthoring

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEmailAuthoringMutationsRecheckAuthorityAfterRootLockIntegration(t *testing.T) {
	t.Run("email template root", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
		mutationDB, applicationName := newEmailAuthoringFenceMutationDB(t)
		service := NewEmailTemplateService(
			mutationDB, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
			WithEmailTemplateContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
			WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
		)
		created, err := service.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
			Key: "fence_template", Name: "Original template", Subject: "Subject", SourceLocale: "en",
		}))
		require.NoError(t, err)

		lockTx := lockEmailAuthoringMutationRoot(t, db, "email_template", "id = '"+created.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			name := "must not persist"
			_, err := service.UpdateEmailTemplate(ctx, connect.NewRequest(&managev1.UpdateEmailTemplateRequest{Id: created.Msg.Id, Name: &name}))
			result <- err
		}()
		requireEmailAuthoringMutationWaitingOnRoot(t, db, applicationName, result)
		demoteEmailAuthoringMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var name string
		require.NoError(t, db.Table("email_template").Select("name").Where("id = ?", created.Msg.Id).Scan(&name).Error)
		require.Equal(t, "Original template", name)
	})

	t.Run("email layout root", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
		mutationDB, applicationName := newEmailAuthoringFenceMutationDB(t)
		service := NewEmailLayoutService(
			mutationDB, "", "", spiceDB,
			WithEmailLayoutCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
			WithEmailLayoutContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		)
		created, err := service.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
			Key: "fence_layout", Name: "Original layout", HtmlContent: "<main>{{content}}</main>", SourceLocale: "en",
		}))
		require.NoError(t, err)

		lockTx := lockEmailAuthoringMutationRoot(t, db, "email_layout", "id = '"+created.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			name := "must not persist"
			_, err := service.UpdateEmailLayout(ctx, connect.NewRequest(&managev1.UpdateEmailLayoutRequest{Id: created.Msg.Id, Name: &name}))
			result <- err
		}()
		requireEmailAuthoringMutationWaitingOnRoot(t, db, applicationName, result)
		demoteEmailAuthoringMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var name string
		require.NoError(t, db.Table("email_layout").Select("name").Where("id = ?", created.Msg.Id).Scan(&name).Error)
		require.Equal(t, "Original layout", name)
	})

	t.Run("email event mapping roots", func(t *testing.T) {
		db := testutil.NewIntegrationDB(t)
		ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
		mutationDB, applicationName := newEmailAuthoringFenceMutationDB(t)
		service := NewEmailTemplateService(
			mutationDB, nil, emailTemplateRuntimeFixture{}, "", "", spiceDB,
			WithEmailTemplateContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
			WithEmailTemplateCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
		)
		created, err := service.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
			Key: "fence_mapping_template", Name: "Mapping template", Subject: "Subject", SourceLocale: "en",
		}))
		require.NoError(t, err)
		var before []string
		require.NoError(t, db.Table("email_template").Where("event_key = ?", "welcome").Order("id").Pluck("id", &before).Error)

		lockTx := lockEmailAuthoringMutationRoot(t, db, "email_template", "id = '"+created.Msg.Id+"'::uuid")
		result := make(chan error, 1)
		go func() {
			_, err := service.UpdateEventMapping(ctx, connect.NewRequest(&managev1.UpdateEventMappingRequest{Event: "welcome", TemplateId: &created.Msg.Id}))
			result <- err
		}()
		requireEmailAuthoringMutationWaitingOnRoot(t, db, applicationName, result)
		demoteEmailAuthoringMutationActor(t, spiceDB, ctx)
		require.NoError(t, lockTx.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

		var after []string
		require.NoError(t, db.Table("email_template").Where("event_key = ?", "welcome").Order("id").Pluck("id", &after).Error)
		require.Equal(t, before, after)
	})
}

func lockEmailAuthoringMutationRoot(t *testing.T, db *gorm.DB, table, condition string) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	var rootID string
	require.NoError(t, tx.Raw("SELECT id::text FROM "+table+" WHERE "+condition+" FOR UPDATE").Scan(&rootID).Error)
	require.NotEmpty(t, rootID)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

func newEmailAuthoringFenceMutationDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := testutil.NewConcurrentPostIntegrationDB(t)
	applicationName := "geul_email_authoring_fence_" + testutil.IntegrationUUID()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db, applicationName
}

func demoteEmailAuthoringMutationActor(t *testing.T, spiceDB *auth.SpiceDBClient, ctx context.Context) {
	t.Helper()
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
}

func requireEmailAuthoringMutationWaitingOnRoot(t *testing.T, db *gorm.DB, applicationName string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			require.FailNow(t, "mutation returned before its authoritative root lock was reached", "error: %v", err)
		default:
		}
		var waiting bool
		err := db.Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE application_name = ? AND wait_event_type = 'Lock'
			  AND cardinality(pg_blocking_pids(pid)) > 0
		)`, applicationName).Scan(&waiting).Error
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("mutation did not reach its authoritative root lock")
}
