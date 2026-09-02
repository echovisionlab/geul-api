//go:build integration

package form_test

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
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestFormAdminMutationRechecksAuthorityAfterRootLockIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	principal := auth.GetUser(ctx)
	require.NotNil(t, principal)
	mutationDB, applicationName := newFormMutationFenceDB(t)
	service := newFormServiceForIntegration(mutationDB, principal.IdentityID.String(), spiceDB)

	slug := "authority-fence-form"
	created, err := service.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{
		Title: "Authority fence Form", Slug: &slug, Schema: integrationFormSchema(),
	}))
	require.NoError(t, err)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() { _ = lockTx.Rollback().Error })
	var lockedID string
	require.NoError(t, lockTx.Raw("SELECT id::text FROM form WHERE id = ?::uuid FOR UPDATE", created.Msg.Id).Scan(&lockedID).Error)
	require.Equal(t, created.Msg.Id, lockedID)

	result := make(chan error, 1)
	go func() {
		isPublic := true
		_, updateErr := service.UpdateForm(ctx, connect.NewRequest(&managev1.UpdateFormRequest{
			Id: created.Msg.Id, IsPublic: &isPublic,
		}))
		result <- updateErr
	}()
	requireFormMutationWaitingOnRoot(t, db, applicationName, result)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
	require.NoError(t, lockTx.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

	var isPublic bool
	require.NoError(t, db.Table("form").Select("is_public").Where("id = ?", created.Msg.Id).Scan(&isPublic).Error)
	require.False(t, isPublic)
}

func newFormMutationFenceDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	lease, err := testutil.CurrentAppIntegrationBackendLease()
	require.NoError(t, err)
	applicationName := "geul_form_admin_fence_" + testutil.IntegrationUUID()
	db, err := gorm.Open(gormpostgres.Open(lease.PostgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db, applicationName
}

func requireFormMutationWaitingOnRoot(t *testing.T, db *gorm.DB, applicationName string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			require.FailNow(t, "mutation returned before its authoritative root lock was reached", "error: %v", err)
		default:
		}
		var waiting bool
		err := db.WithContext(context.Background()).Raw(`SELECT EXISTS (
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
