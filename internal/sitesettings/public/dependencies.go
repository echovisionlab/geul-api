package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MenuSlots is the selected, localized and target-filtered Menu projection.
type MenuSlots struct {
	Header         []model.MenuItem
	Secondary      []model.MenuItem
	Footer         []model.MenuItem
	AvatarDropdown []model.MenuItem
}

// ManifestProjection owns concrete persistence, asset URL and target-route
// reads required by the public Site Settings policy service.
type ManifestProjection interface {
	Settings(context.Context) (*model.SiteSettings, error)
	Menus(context.Context, *model.SiteSettings, string) (MenuSlots, error)
	ReadySourceAsset(context.Context, string, string) *commonv1.AssetRef
	ReadyAsset(context.Context, string) *commonv1.AssetRef
	Favicon(context.Context, string) (*commonv1.AssetRef, *commonv1.FaviconAssetSet)
	LoaderAssets(context.Context) ([]*commonv1.AssetRef, error)
	TargetSlug(context.Context, *model.MenuItem) *string
}
