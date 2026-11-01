package sitesettings

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	domain "github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	"gorm.io/gorm"
)

// PublicProjection owns the DB, CDN asset and target-route reads used to build
// the public Site Settings manifest.
type PublicProjection struct {
	db     *gorm.DB
	assets Assets
	menus  ManifestMenus
}

func NewPublicProjection(db *gorm.DB, assets Assets, menus ManifestMenus) *PublicProjection {
	if db == nil {
		panic("site settings public projection dependencies are required")
	}
	return &PublicProjection{db: db, assets: assets, menus: menus}
}

var _ publicsitesettings.ManifestProjection = (*PublicProjection)(nil)

func (p *PublicProjection) Settings(ctx context.Context) (domain.Settings, error) {
	var settings model.SiteSettings
	if err := p.db.WithContext(ctx).First(&settings, "id = 1").Error; err != nil {
		return domain.Settings{}, err
	}
	return sitesettingspostgres.SettingsFromRow(settings), nil
}

func (p *PublicProjection) Menus(
	ctx context.Context,
	settings domain.Settings,
	acceptLanguage string,
) (publicsitesettings.MenuSlots, error) {
	menuIDs := selectedManifestMenuIDs(settings)
	var menus []model.Menu
	if len(menuIDs) > 0 {
		if err := p.db.WithContext(ctx).Where("id IN ?", menuIDs).Find(&menus).Error; err != nil {
			return publicsitesettings.MenuSlots{}, err
		}
	}
	byID := make(map[string]model.Menu, len(menus))
	for _, menu := range menus {
		byID[menu.ID] = menu
	}
	load := func(menuID *string) ([]publicsitesettings.MenuItem, error) {
		if menuID == nil || strings.TrimSpace(*menuID) == "" {
			return nil, nil
		}
		menu, ok := byID[*menuID]
		if !ok {
			return nil, nil
		}
		var items []model.MenuItem
		if err := json.Unmarshal(menu.Items, &items); err != nil {
			slog.WarnContext(ctx, "Stored menu items are invalid; serving the manifest without the menu", "menu_id", menu.ID, failurecause.Attr(err))
			return nil, nil
		}
		localized, err := p.menus.Localize(ctx, p.db, menu.ID, items, acceptLanguage)
		if err != nil {
			slog.WarnContext(ctx, "Menu localization failed; serving source menu items in the manifest", "menu_id", menu.ID, failurecause.Attr(err))
		} else {
			items = localized
		}
		published, err := p.menus.PublishedPageTargets(ctx, p.db, items)
		return manifestMenuItems(published), err
	}
	var result publicsitesettings.MenuSlots
	var err error
	if result.Header, err = load(settings.MenuHeaderID); err != nil {
		return result, err
	}
	if result.Secondary, err = load(settings.MenuSecondaryID); err != nil {
		return result, err
	}
	if result.Footer, err = load(settings.MenuFooterID); err != nil {
		return result, err
	}
	if result.AvatarDropdown, err = load(settings.MenuAvatarDropdownID); err != nil {
		return result, err
	}
	return result, nil
}

// manifestMenuItems maps the stored items to the manifest's; nil stays nil.
func manifestMenuItems(items []model.MenuItem) []publicsitesettings.MenuItem {
	if items == nil {
		return nil
	}
	result := make([]publicsitesettings.MenuItem, 0, len(items))
	for _, item := range items {
		mapped := publicsitesettings.MenuItem{
			ID: item.ID, Label: item.Label, LinkType: item.LinkType,
			URL: item.URL, TargetID: item.TargetID, TargetSlug: item.TargetSlug,
			OpenInNewTab: item.OpenInNewTab, Children: manifestMenuItems(item.Children),
		}
		if item.Visibility != nil {
			mapped.Visibility = &publicsitesettings.MenuVisibility{Mode: item.Visibility.Mode, Roles: item.Visibility.Roles}
		}
		result = append(result, mapped)
	}
	return result
}

func selectedManifestMenuIDs(settings domain.Settings) []string {
	ids := make([]string, 0, 4)
	for _, id := range []*string{
		settings.MenuHeaderID,
		settings.MenuSecondaryID,
		settings.MenuFooterID,
		settings.MenuAvatarDropdownID,
	} {
		if id != nil && strings.TrimSpace(*id) != "" {
			ids = append(ids, *id)
		}
	}
	return ids
}

func (p *PublicProjection) ReadySourceAsset(ctx context.Context, fileID, kind string) *domain.Asset {
	asset, err := p.assets.ReadyRef(ctx, p.db, fileID, kind)
	if err != nil {
		slog.Log(ctx, errs.OperationLogLevel(err), "Site Settings public asset resolution failed; serving Site Settings without the asset", failurecause.Attr(err), "file_id", fileID, "kind", kind)
		return nil
	}
	return sitesettingspostgres.AssetFromRef(asset)
}

func (p *PublicProjection) ReadyAsset(ctx context.Context, assetID string) *domain.Asset {
	asset, err := p.assets.ReadyAsset(ctx, p.db, assetID)
	if err != nil {
		slog.Log(ctx, errs.OperationLogLevel(err), "Site Settings public asset resolution failed; serving Site Settings without the asset", failurecause.Attr(err), "asset_id", assetID)
		return nil
	}
	return sitesettingspostgres.AssetFromRef(asset)
}

func (p *PublicProjection) Favicon(ctx context.Context, fileID string) (*domain.Asset, *domain.FaviconSet) {
	asset, set := p.assets.ProjectFavicon(ctx, p.db, fileID)
	return sitesettingspostgres.AssetFromRef(asset), sitesettingspostgres.FaviconSetFromRef(set)
}

func (p *PublicProjection) LoaderAssets(ctx context.Context) ([]domain.Asset, error) {
	var rows []struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := p.db.WithContext(ctx).Table("site_setting_loader_file AS slf").
		Select("slf.file_id").Where("slf.site_setting_id = ?", 1).
		Order("slf.position ASC, slf.created_at ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	assets := make([]domain.Asset, 0, len(rows))
	for _, row := range rows {
		if asset := p.ReadySourceAsset(ctx, row.FileID, "loader"); asset != nil {
			assets = append(assets, *asset)
		}
	}
	return assets, nil
}

func (p *PublicProjection) TargetSlug(ctx context.Context, item publicsitesettings.MenuItem) *string {
	return p.menus.TargetSlug(ctx, p.db, &model.MenuItem{LinkType: item.LinkType, TargetID: item.TargetID, TargetSlug: item.TargetSlug})
}
