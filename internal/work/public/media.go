package public

import (
	"context"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// MediaResolver is the Work public-read projection supplied by its runtime adapter.
type MediaResolver interface {
	ResolveReadyOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error)
	IsReadyAsset(context.Context, *gorm.DB, string) (bool, error)
	ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef
	ResolveArtistImageAsset(context.Context, string) *commonv1.AssetRef
}
