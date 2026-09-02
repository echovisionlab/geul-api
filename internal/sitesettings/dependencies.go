package sitesettings

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// AssetBinding identifies a public asset usage owned by Site Settings.
type AssetBinding struct {
	SourceFileID string
	Key          string
	Kind         string
}

// Assets supplies the file-owned lifecycle operations required by Site Settings.
type Assets interface {
	ValidateAttachment(context.Context, *gorm.DB, string, string) error
	LockForAttachment(context.Context, *gorm.DB, []string) error
	BindReady(context.Context, *gorm.DB, AssetBinding) (*commonv1.AssetRef, error)
	Release(context.Context, *gorm.DB, string) error
	ReplaceFavicon(context.Context, *gorm.DB, *string) error
	ReadyRef(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ReadyAsset(context.Context, *gorm.DB, string) (*commonv1.AssetRef, error)
	ProjectFavicon(context.Context, *gorm.DB, string) (*commonv1.AssetRef, *commonv1.FaviconAssetSet)
}

// References validates concrete Page and Menu selections using the caller's
// transaction. Site Settings owns which keys may reference them.
type References interface {
	Validate(context.Context, *gorm.DB, string, *string) error
}

// OGInvalidator plans Site Settings-owned OG regeneration in the caller's
// transaction and returns the resulting run ID when work was requested.
type OGInvalidator interface {
	Request(
		context.Context,
		*gorm.DB,
		*model.SiteSettings,
		*model.SiteSettings,
		[]string,
	) (*string, error)
}
