package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/sitesettings"
)

// MenuVisibility says who sees a Menu item: "all" (or empty), "authenticated",
// "guest" or "roles" with the platform roles that do.
type MenuVisibility struct {
	Mode  string
	Roles []string
}

// MenuItem is an item of a selected Menu, localized and with its published
// target.
type MenuItem struct {
	ID           string
	Label        string
	LinkType     string // custom, page, category, tag, series
	URL          *string
	TargetID     *string
	TargetSlug   *string
	OpenInNewTab *bool
	Visibility   *MenuVisibility
	Children     []MenuItem
}

// MenuSlots is the selected, localized and target-filtered Menu projection.
type MenuSlots struct {
	Header         []MenuItem
	Secondary      []MenuItem
	Footer         []MenuItem
	AvatarDropdown []MenuItem
}

// ManifestProjection owns concrete persistence, asset URL and target-route
// reads required by the public Site Settings policy service.
type ManifestProjection interface {
	Settings(context.Context) (sitesettings.Settings, error)
	Menus(ctx context.Context, settings sitesettings.Settings, acceptLanguage string) (MenuSlots, error)
	// ReadySourceAsset, ReadyAsset and Favicon log a failure and answer
	// none.
	ReadySourceAsset(ctx context.Context, fileID, kind string) *sitesettings.Asset
	ReadyAsset(ctx context.Context, assetID string) *sitesettings.Asset
	Favicon(ctx context.Context, fileID string) (*sitesettings.Asset, *sitesettings.FaviconSet)
	// LoaderAssets lists the ready assets of the loader Files in order.
	LoaderAssets(context.Context) ([]sitesettings.Asset, error)
	// TargetSlug is the route of item's target, or its stored slug.
	TargetSlug(ctx context.Context, item MenuItem) *string
}
