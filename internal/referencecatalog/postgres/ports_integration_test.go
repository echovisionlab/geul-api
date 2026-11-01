//go:build integration

package referencecatalogpostgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/testutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests run the use cases over the production Store and the media and
// Menu adapters it calls in the catalog transaction, on PostgreSQL only:
// authorization and the fence are fakes, because what they pin is how the
// catalog's binding keys, release prefixes and menu rewrites land in the
// tables those adapters own, which neither the fake Store nor the contract
// models.

type ports struct {
	db      *gorm.DB
	ctx     context.Context
	service func(audit *audittest.Recorder) *referencecatalog.Service
}

func newPorts(t *testing.T) ports {
	t.Helper()
	db := newDatabase(t)
	identityID := uuid.NewString()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Catalog editor")
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.72")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return ports{db: db, ctx: ctx, service: func(audit *audittest.Recorder) *referencecatalog.Service {
		service, err := referencecatalog.NewService(referencecatalog.Deps{
			Units: newUnits(t, db, audit), SpiceDB: authztest.AllowAll(), Fence: &identitystatetest.Fence{}, Audit: audit,
		})
		require.NoError(t, err)
		return service
	}}
}

// bindings maps each binding key of owner to its asset.
func (p ports) bindings(t *testing.T, ownerType, ownerID string) map[string]string {
	t.Helper()
	var rows []model.PublicAssetBinding
	require.NoError(t, p.db.Where("owner_type = ? AND owner_id = ?", ownerType, ownerID).Find(&rows).Error)
	bound := map[string]string{}
	for _, row := range rows {
		bound[row.BindingKey] = row.AssetID
	}
	return bound
}

func (p ports) assetStatus(t *testing.T, assetID string) string {
	t.Helper()
	var status string
	require.NoError(t, p.db.Table("public_asset").Select("status").Where("id = ?", assetID).Scan(&status).Error)
	return status
}

func (p ports) assetURL(t *testing.T, fileID, kind string) string {
	t.Helper()
	asset, err := mediaasset.ReadyPublicAssetRefForSourceFile(t.Context(), p.db, "https://cdn.example.com", fileID, kind)
	require.NoError(t, err)
	return asset.GetUrl()
}

func TestClientLogoSlotsShareAFileWithoutReleasingEachOtherIntegration(t *testing.T) {
	t.Parallel()
	p := newPorts(t)
	service := p.service(&audittest.Recorder{})
	fileID, assetID := fixtures{db: p.db}.readyFile(t, "logo")
	created, err := service.CreateClient(p.ctx, referencecatalog.CreateClientCommand{Name: "Shared logo client"})
	require.NoError(t, err)
	clientID := created.Client.ID

	for _, slot := range []referencecatalog.ClientLogoSlot{referencecatalog.ClientLogoLight, referencecatalog.ClientLogoDark} {
		bound, err := service.SetClientLogo(p.ctx, referencecatalog.SetClientLogoCommand{ClientID: clientID, Slot: slot, FileID: fileID})
		require.NoError(t, err)
		require.Equal(t, p.assetURL(t, fileID, "logo"), bound.GetUrl())
	}
	require.Equal(t, map[string]string{"logo_light": assetID, "logo_dark": assetID}, p.bindings(t, "client", clientID))
	view, err := service.GetClient(p.ctx, referencecatalog.GetClientQuery{ID: clientID})
	require.NoError(t, err)
	require.Equal(t, view.LightLogo.GetUrl(), view.DarkLogo.GetUrl())

	require.NoError(t, service.DeleteClientLogo(p.ctx, referencecatalog.DeleteClientLogoCommand{ClientID: clientID, Slot: referencecatalog.ClientLogoDark}))
	require.Equal(t, map[string]string{"logo_light": assetID}, p.bindings(t, "client", clientID), "the dark slot releases only its own binding")
	require.Equal(t, model.PublicAssetStatusReady, p.assetStatus(t, assetID))

	require.NoError(t, service.DeleteClient(p.ctx, referencecatalog.DeleteClientCommand{ID: clientID}))
	require.Empty(t, p.bindings(t, "client", clientID), "deleting the Client releases every logo binding")
	var files int64
	require.NoError(t, p.db.Table("file").Where("id = ?", fileID).Count(&files).Error)
	require.EqualValues(t, 1, files, "the shared file is kept")
}

func TestMapPlaceImageBindingFollowsTheSelectedFileIntegration(t *testing.T) {
	t.Parallel()
	p := newPorts(t)
	service := p.service(&audittest.Recorder{})
	firstFileID, firstAssetID := fixtures{db: p.db}.readyFile(t, "map_image")
	secondFileID, secondAssetID := fixtures{db: p.db}.readyFile(t, "map_image")

	created, err := service.CreateMapPlace(p.ctx, referencecatalog.CreateMapPlaceCommand{
		Name: "Imaged place", Address: "1 Asset Road", Lat: 37.5, Lng: 127, ImageFileID: &firstFileID,
	})
	require.NoError(t, err)
	placeID := created.Place.ID
	require.Equal(t, p.assetURL(t, firstFileID, "map_image"), created.ImageAsset.GetUrl())
	require.Equal(t, "Catalog editor", created.CreatedByMember.Nickname)
	require.Equal(t, map[string]string{"image": firstAssetID}, p.bindings(t, "map_place", placeID))

	updated, err := service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: placeID, ImageFileID: &secondFileID})
	require.NoError(t, err)
	require.Equal(t, p.assetURL(t, secondFileID, "map_image"), updated.ImageAsset.GetUrl())
	require.Equal(t, map[string]string{"image": secondAssetID}, p.bindings(t, "map_place", placeID))
	require.Equal(t, model.PublicAssetStatusReady, p.assetStatus(t, firstAssetID))

	cleared, err := service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: placeID, ClearImage: true})
	require.NoError(t, err)
	require.Nil(t, cleared.ImageAsset)
	require.Empty(t, p.bindings(t, "map_place", placeID))

	_, err = service.UpdateMapPlace(p.ctx, referencecatalog.UpdateMapPlaceCommand{ID: placeID, ImageFileID: &firstFileID})
	require.NoError(t, err)
	require.NoError(t, service.DeleteMapPlace(p.ctx, referencecatalog.DeleteMapPlaceCommand{ID: placeID}))
	require.Empty(t, p.bindings(t, "map_place", placeID))
	require.Equal(t, model.PublicAssetStatusReady, p.assetStatus(t, firstAssetID))
	require.Equal(t, model.PublicAssetStatusReady, p.assetStatus(t, secondAssetID))
}

func (p ports) menu(t *testing.T, item model.MenuItem) string {
	t.Helper()
	items, err := json.Marshal([]model.MenuItem{item})
	require.NoError(t, err)
	now := time.Now().UTC()
	documentID := uuid.NewString()
	require.NoError(t, p.db.Exec(`INSERT INTO content_document (id, profile, created_at, updated_at) VALUES (?::uuid, 'compact', ?, ?)`, documentID, now, now).Error)
	menu := model.Menu{
		ID: uuid.NewString(), ContentDocumentID: documentID, SourceLocale: "en", Name: "Menu " + item.ID,
		Items: items, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, p.db.Create(&menu).Error)
	return menu.ID
}

func (p ports) menuItems(t *testing.T, menuID string) string {
	t.Helper()
	var items string
	require.NoError(t, p.db.Table("menu").Select("items::text").Where("id = ?", menuID).Scan(&items).Error)
	return items
}

func TestMenuTargetsMoveAndRollBackWithTheReferenceIntegration(t *testing.T) {
	t.Parallel()
	p := newPorts(t)
	audit := &audittest.Recorder{}
	service := p.service(audit)
	categorySlug, tagSlug := "menu-category", "menu-tag"
	category, err := service.CreateCategory(p.ctx, referencecatalog.CreateReferenceCommand{Name: "Menu category", Slug: &categorySlug})
	require.NoError(t, err)
	tag, err := service.CreateTag(p.ctx, referencecatalog.CreateReferenceCommand{Name: "Menu tag", Slug: &tagSlug})
	require.NoError(t, err)
	categoryMenu := p.menu(t, model.MenuItem{ID: "category-target", Label: "Category", LinkType: "category", TargetID: &category.ID, TargetSlug: &categorySlug})
	tagMenu := p.menu(t, model.MenuItem{ID: "tag-target", Label: "Tag", LinkType: "tag", TargetID: &tag.ID, TargetSlug: &tagSlug})

	failing := p.service(&audittest.Recorder{Err: errors.New("audit unavailable")})
	rolledBack := "menu-category-rolled-back"
	_, err = failing.UpdateCategory(p.ctx, referencecatalog.UpdateReferenceCommand{ID: category.ID, Slug: &rolledBack})
	require.Error(t, err)
	require.Contains(t, p.menuItems(t, categoryMenu), `"`+categorySlug+`"`, "a rolled back slug change keeps the menu target")

	moved := "menu-category-moved"
	_, err = service.UpdateCategory(p.ctx, referencecatalog.UpdateReferenceCommand{ID: category.ID, Slug: &moved})
	require.NoError(t, err)
	require.Contains(t, p.menuItems(t, categoryMenu), `"`+moved+`"`)
	require.NoError(t, service.DeleteTag(p.ctx, referencecatalog.DeleteReferenceCommand{ID: tag.ID}))
	require.NotContains(t, p.menuItems(t, tagMenu), tag.ID)

	var menuAudits []string
	for _, record := range audit.Records() {
		if record.Action == sharedtelemetry.AuditMenuUpdated {
			menuAudits = append(menuAudits, record.TargetID)
		}
	}
	require.ElementsMatch(t, []string{categoryMenu, tagMenu}, menuAudits, "each rewritten Menu is audited in the catalog transaction")
}
