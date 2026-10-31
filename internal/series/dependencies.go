package series

import (
	"context"
	"errors"
	"time"

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
	RequireLockedEdit(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
}

// PostMembership reads and writes the Post rows of a membership change. Post
// owns those rows and keeps its invariants (updated_at) when their Series
// relation changes.
type PostMembership interface {
	ListSeriesPosts(ctx context.Context, db *gorm.DB, seriesID string) ([]SeriesPost, error)
	SeriesPostIDs(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error)
	// ReadSeriesRelation and LockSeriesRelation report gorm.ErrRecordNotFound
	// for a missing Post.
	ReadSeriesRelation(ctx context.Context, db *gorm.DB, postID string) (*string, error)
	LockSeriesRelation(ctx context.Context, tx *gorm.DB, postID string) (*string, error)
	// LockSeriesPosts locks the Posts in ID order and reports how many exist.
	LockSeriesPosts(ctx context.Context, tx *gorm.DB, postIDs []string) (int, error)
	NextSeriesOrder(ctx context.Context, tx *gorm.DB, seriesID string) (int, error)
	// AssignSeries reports gorm.ErrRecordNotFound for a missing Post.
	AssignSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, order int, now time.Time) error
	// ClearSeries and OrderSeries report ErrPostRelationChanged for a Post
	// that is no longer in the Series.
	ClearSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, now time.Time) error
	ClearAllSeries(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error
	OrderSeries(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string, now time.Time) error
}

// ErrPostRelationChanged reports a Post that left the Series it was read in.
var ErrPostRelationChanged = errors.New("post series relation changed")

// SeriesPost is a Post of a Series with its own source locale.
type SeriesPost struct {
	ID           string
	Title        string
	SourceLocale string
	Slug         *string
	Status       string
	SeriesOrder  *int
	PublishedAt  *time.Time
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
