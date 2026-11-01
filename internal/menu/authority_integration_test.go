//go:build integration

package menu_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// These tests need what no fake has: SpiceDB relationship semantics, and
// authority read after a real row lock wait through the Kratos-backed fence.

func newPostgresMenuService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *menu.Service {
	t.Helper()
	audit := &audittest.Recorder{}
	service, err := menu.NewService(menu.Deps{
		Units:   menupostgres.NewUnits(db, menupostgres.Ports{SiteSettings: menuadapter.NewSiteSettingsReferences(audit)}),
		SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
	})
	require.NoError(t, err)
	return service
}

func TestMenuUpdateRejectsAdminRevokedWhileRootLockWaitsIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := testutil.NewAuditContext(t, actor.IdentityID, actor.MemberID)
	service := newPostgresMenuService(t, stack.DB, stack.SpiceDBClient)
	created, err := service.CreateMenu(ctx, menu.CreateMenuCommand{Name: "authority-fence-menu", Items: []menu.Item{customItem("before", "/before")}})
	require.NoError(t, err)

	lock := stack.DB.Begin()
	require.NoError(t, lock.Error)
	t.Cleanup(func() { _ = lock.Rollback().Error })
	var locked model.Menu
	require.NoError(t, lock.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&locked, "id = ?", created.ID).Error)

	result := make(chan error, 1)
	go func() {
		_, updateErr := service.UpdateMenu(ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer("must-not-persist")})
		result <- updateErr
	}()
	testutil.RequireLockWaiters(t, stack.DB, 1)

	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(actor.IdentityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.User())
	require.NoError(t, err)
	require.NoError(t, lock.Rollback().Error)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
	var persistedName string
	require.NoError(t, stack.DB.Table("menu").Select("name").Where("id = ?", created.ID).Scan(&persistedName).Error)
	require.Equal(t, "authority-fence-menu", persistedName)
}

func TestMenuCreateAndDeleteWriteAndRemoveThePolicyRelationshipIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := testutil.NewAuditContext(t, admin.IdentityID, admin.MemberID)
	spiceDB := stack.SpiceDBClient
	service := newPostgresMenuService(t, stack.DB, spiceDB)
	actor, err := policyv1.NewAccountIdentityActor(admin.IdentityID)
	require.NoError(t, err)
	canManage := func(menuID string) bool {
		can, err := policyv1.Menu.Manage(menuID)
		require.NoError(t, err)
		allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
		require.NoError(t, err)
		return allowed
	}

	created, err := service.CreateMenu(ctx, menu.CreateMenuCommand{Name: "Authorization lifecycle menu", Items: []menu.Item{customItem("home", "/")}})
	require.NoError(t, err)
	require.True(t, canManage(created.ID))

	require.NoError(t, service.DeleteMenu(ctx, menu.DeleteMenuCommand{ID: created.ID}))
	require.False(t, canManage(created.ID))
}
