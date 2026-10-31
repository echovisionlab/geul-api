//go:build integration

package menu_test

import (
	"encoding/json"
	"testing"
	"time"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"gorm.io/gorm"

	"connectrpc.com/connect"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

func TestCategoryTargetRewriteWaitsForMenuWriterIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewConcurrentPostIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu lock admin "+identityID[:8])
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	menuService := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)
	categoryService := referencecatalog.NewCategoryConnectHandler(referenceCatalogForTest(t, db, "", spiceDB))
	suffix := identityID[:8]
	categorySlug := "menu-lock-category-" + suffix
	category, err := categoryService.CreateCategory(ctx, connect.NewRequest(&managev1.CreateCategoryRequest{
		Name: "Menu Lock Category " + suffix,
		Slug: &categorySlug,
	}))
	require.NoError(t, err)
	menu, err := menuService.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "Menu Lock " + suffix,
	}))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM menu WHERE id = ?", menu.Msg.Id).Error
		_ = db.Exec("DELETE FROM category WHERE id = ?", category.Msg.Id).Error
	})

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	var locked model.Menu
	require.NoError(t, blocker.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&locked, "id = ?", menu.Msg.Id).Error)
	items, err := json.Marshal([]model.MenuItem{{
		ID: "category", Label: "Category", LinkType: "category",
		TargetID: &category.Msg.Id, TargetSlug: &categorySlug,
	}})
	require.NoError(t, err)
	require.NoError(t, blocker.Model(&model.Menu{}).Where("id = ?", menu.Msg.Id).Update("items", items).Error)

	nextSlug := "menu-lock-category-updated-" + suffix
	result := make(chan error, 1)
	go func() {
		_, updateErr := categoryService.UpdateCategory(ctx, connect.NewRequest(&managev1.UpdateCategoryRequest{
			Id: category.Msg.Id, Slug: &nextSlug,
		}))
		result <- updateErr
	}()
	requireMenuCallStillWaiting(t, result)
	require.NoError(t, blocker.Commit().Error)
	require.NoError(t, requireMenuCallResult(t, result))

	fetched, err := menuService.GetMenuById(ctx, connect.NewRequest(&managev1.GetMenuByIdRequest{Id: menu.Msg.Id}))
	require.NoError(t, err)
	require.Len(t, fetched.Msg.Items, 1)
	require.Equal(t, nextSlug, fetched.Msg.Items[0].GetTargetSlug())
}

func requireMenuCallStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "call returned before the Menu row lock was released", "error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireMenuCallResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for Menu target update")
		return nil
	}
}

// referenceCatalogForTest builds the Reference Catalog use cases with the
// production adapters and in-memory audit.
func referenceCatalogForTest(t *testing.T, db *gorm.DB, cdnDomain string, spiceDB *auth.SpiceDBClient) *referencecatalog.Service {
	t.Helper()
	catalog, err := referencecatalog.NewService(referencecatalog.Deps{
		DB: db, SpiceDB: spiceDB, Audit: &audittest.Recorder{},
		Assets:      referencecatalogadapter.NewAssets(cdnDomain),
		Members:     referencecatalogadapter.NewMemberSummaries(cdnDomain),
		MenuTargets: referencecatalogmenuadapter.NewTargets(nil),
		ClientWorks: workadapter.ClientWorks{},
	})
	require.NoError(t, err)
	return catalog
}
