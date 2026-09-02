//go:build integration

package menu

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestMenuUpdateRejectsAdminRevokedWhileRootLockWaitsIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(t.Context(), actor.AuthUserInfo())
	service := NewMenuService(
		stack.DB,
		noopMenuSiteSettingsReferences{},
		noopMenuTargetReferences{},
		stack.SpiceDBClient,
	)
	url := "/before"
	menu, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "authority-fence-menu",
		Items: []*managev1.MenuItem{{
			Id: "before", Label: "Before", LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM, Url: &url,
		}},
	}))
	require.NoError(t, err)

	lock := stack.DB.Begin()
	require.NoError(t, lock.Error)
	t.Cleanup(func() { _ = lock.Rollback().Error })
	var locked model.Menu
	require.NoError(t, lock.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", menu.Msg.Id).Error)

	result := make(chan error, 1)
	name := "must-not-persist"
	go func() {
		_, updateErr := service.UpdateMenu(ctx, connect.NewRequest(&managev1.UpdateMenuRequest{Id: menu.Msg.Id, Name: &name}))
		result <- updateErr
	}()
	requirePendingAdvisoryOrRowLock(t, stack.DB)

	demoteIntegrationIdentity(t, stack, actor.IdentityID)
	require.NoError(t, lock.Rollback().Error)

	err = <-result
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var persistedName string
	require.NoError(t, stack.DB.Table("menu").Select("name").Where("id = ?", menu.Msg.Id).Scan(&persistedName).Error)
	require.Equal(t, "authority-fence-menu", persistedName)
}

type noopMenuSiteSettingsReferences struct{}

func (noopMenuSiteSettingsReferences) ClearMenuReferences(context.Context, *gorm.DB, string) error {
	return nil
}

type noopMenuTargetReferences struct{}

func (noopMenuTargetReferences) ValidateAndLock(context.Context, *gorm.DB, []TargetReference) error {
	return nil
}

func demoteIntegrationIdentity(t *testing.T, stack *testutil.OryStack, identityID string) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.User())
	require.NoError(t, err)
}

func requirePendingAdvisoryOrRowLock(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := db.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE wait_event_type = 'Lock'
				  AND cardinality(pg_blocking_pids(pid)) > 0
			)`).Scan(&waiting).Error
		return err == nil && waiting
	}, 5*time.Second, 20*time.Millisecond)
}
