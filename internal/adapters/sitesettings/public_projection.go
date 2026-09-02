package sitesettings

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	domain "github.com/echovisionlab/geul-api/internal/sitesettings"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// PublicProjection owns the DB, CDN asset and target-route reads used to build
// the public Site Settings manifest.
type PublicProjection struct {
	db     *gorm.DB
	assets domain.Assets
	menus  ManifestMenus
}

func NewPublicProjection(db *gorm.DB, assets domain.Assets, menus ManifestMenus) *PublicProjection {
	if db == nil || assets == nil {
		panic("site settings public projection dependencies are required")
	}
	return &PublicProjection{db: db, assets: assets, menus: menus}
}

var _ publicsitesettings.ManifestProjection = (*PublicProjection)(nil)

func (p *PublicProjection) Settings(ctx context.Context) (*model.SiteSettings, error) {
	var settings model.SiteSettings
	if err := p.db.WithContext(ctx).First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func (p *PublicProjection) Menus(
	ctx context.Context,
	settings *model.SiteSettings,
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
	load := func(menuID *string) ([]model.MenuItem, error) {
		if menuID == nil || strings.TrimSpace(*menuID) == "" {
			return nil, nil
		}
		menu, ok := byID[*menuID]
		if !ok {
			return nil, nil
		}
		var items []model.MenuItem
		if err := json.Unmarshal(menu.Items, &items); err != nil {
			slog.Warn("failed to unmarshal menu items for manifest", "menu_id", menu.ID, "error", err)
			return nil, nil
		}
		localized, err := p.menus.Localize(ctx, p.db, menu.ID, items, acceptLanguage)
		if err != nil {
			slog.Warn("failed to project localized menu items for manifest", "menu_id", menu.ID, "error", err)
		} else {
			items = localized
		}
		return p.menus.PublishedPageTargets(ctx, p.db, items)
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

func selectedManifestMenuIDs(settings *model.SiteSettings) []string {
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

func (p *PublicProjection) ReadySourceAsset(ctx context.Context, fileID, kind string) *commonv1.AssetRef {
	asset, err := p.assets.ReadyRef(ctx, p.db, fileID, kind)
	if err != nil {
		return nil
	}
	return asset
}

func (p *PublicProjection) ReadyAsset(ctx context.Context, assetID string) *commonv1.AssetRef {
	asset, err := p.assets.ReadyAsset(ctx, p.db, assetID)
	if err != nil {
		return nil
	}
	return asset
}

func (p *PublicProjection) Favicon(
	ctx context.Context,
	fileID string,
) (*commonv1.AssetRef, *commonv1.FaviconAssetSet) {
	return p.assets.ProjectFavicon(ctx, p.db, fileID)
}

func (p *PublicProjection) LoaderAssets(ctx context.Context) ([]*commonv1.AssetRef, error) {
	var rows []struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := p.db.WithContext(ctx).Table("site_setting_loader_file AS slf").
		Select("slf.file_id").Where("slf.site_setting_id = ?", 1).
		Order("slf.position ASC, slf.created_at ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	assets := make([]*commonv1.AssetRef, 0, len(rows))
	for _, row := range rows {
		if asset := p.ReadySourceAsset(ctx, row.FileID, "loader"); asset != nil {
			assets = append(assets, asset)
		}
	}
	return assets, nil
}

func (p *PublicProjection) TargetSlug(ctx context.Context, item *model.MenuItem) *string {
	return p.menus.TargetSlug(ctx, p.db, item)
}
