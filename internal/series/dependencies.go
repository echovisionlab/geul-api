package series

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// MenuTargets keeps Menu-owned Series links synchronized in the caller transaction.
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, string, string, string, string) error
	Remove(context.Context, *gorm.DB, string, string, string) error
}

// PostAccess owns Post edit authorization used by Series membership changes.
type PostAccess interface {
	PostSourceTitleSQL() string
	RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
}

// MemberSummaries projects durable Series manager attribution.
type MemberSummaries interface {
	LoadSeriesManagers(context.Context, []string) (map[string]*managev1.SeriesManager, error)
}

// MediaRuntime adapts File/PublicAsset persistence and CDN projection to the
// Series application without exposing media implementation packages.
type MediaRuntime interface {
	ReadyAsset(context.Context, ...*string) (*commonv1.AssetRef, error)
	ReadyAssets(context.Context, ...*string) (map[string]*commonv1.AssetRef, error)
	ReadyAssetsForSourceFiles(context.Context, string, []string) (map[string]*commonv1.AssetRef, error)
	ReadyAssetForSourceFile(context.Context, string, string) (*commonv1.AssetRef, error)
	RequireAttachableFile(context.Context, *gorm.DB, string) error
	BindFeaturedImage(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ReleaseFeaturedImage(context.Context, *gorm.DB, string) error
	CancelAndReleaseOG(context.Context, *gorm.DB, string) error
}

// OGRefresh requests the Series-owned OG targets through the shared OG runtime.
type OGRefresh interface {
	RequestCurrent(context.Context, *gorm.DB, string, string, bool, string) (*string, error)
}

// SeriesReadProjection loads cross-domain aggregate counts without exposing
// Post or manager persistence to the Series application.
type SeriesReadProjection interface {
	LoadPostCounts(context.Context, []string) (map[string]int32, error)
	LoadManagerCounts(context.Context, []string) (map[string]int32, error)
}

type SeriesListDetails struct {
	PostCount          int32
	ManagerCount       int32
	FeaturedImageAsset *commonv1.AssetRef
}
