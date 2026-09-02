//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestSiteSettingsMutationRechecksAuthorityAfterRootLockIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	mutationDB, applicationName := newSiteSettingsFenceMutationDB(t)
	service := sitesettings.NewSiteSettingService(
		mutationDB,
		"https://www.example.test",
		sitesettingsadapter.NewAssets("https://cdn.example.test"),
		sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"),
		spiceDB,
	)

	lockTx := lockSiteSettingsMutationRoot(t, db, "site_settings", "id = 1")
	result := make(chan error, 1)
	go func() {
		_, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
			Key: "company_name", Value: structpb.NewStringValue("must not persist"),
		}))
		result <- err
	}()
	requireSiteSettingsMutationWaitingOnRoot(t, db, applicationName, result)
	demoteSiteSettingsMutationActor(t, spiceDB, ctx)
	require.NoError(t, lockTx.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

	var companyName string
	require.NoError(t, db.Table("site_settings").Select("company_name").Where("id = 1").Scan(&companyName).Error)
	require.NotEqual(t, "must not persist", companyName)
}

func lockSiteSettingsMutationRoot(t *testing.T, db *gorm.DB, table, condition string) *gorm.DB {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	var rootID string
	require.NoError(t, tx.Raw("SELECT id::text FROM "+table+" WHERE "+condition+" FOR UPDATE").Scan(&rootID).Error)
	require.NotEmpty(t, rootID)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

func newSiteSettingsFenceMutationDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	db := testutil.NewConcurrentPostIntegrationDB(t)
	applicationName := "geul_site_settings_fence_" + testutil.IntegrationUUID()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db, applicationName
}

func demoteSiteSettingsMutationActor(t *testing.T, spiceDB *auth.SpiceDBClient, ctx context.Context) {
	t.Helper()
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
}

func requireSiteSettingsMutationWaitingOnRoot(t *testing.T, db *gorm.DB, applicationName string, result <-chan error) {
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
