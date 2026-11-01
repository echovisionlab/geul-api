package public

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Store is what the public Post reads read, bound to one read snapshot or
// transaction. postpublicpostgres implements it over the post tables and
// the localization, share link and map place runtimes a public read uses.
type Store interface {
	post.CommentReads
	post.ContentReads
	// SourceLocaleStates reads the source locale row of each Post that has
	// one.
	SourceLocaleStates(ctx context.Context, postIDs []string) (map[string]post.SourceLocaleState, error)
	// AuthorLinks lists the authors of each Post, oldest first.
	AuthorLinks(ctx context.Context, postIDs []string) ([]post.AuthorLink, error)
	// ShareLockPost share-locks the Post named by its ID when byID is set,
	// else by its slug, and reads it with its relations and Map Place. A
	// missing Post is post.ErrPostNotFound.
	ShareLockPost(ctx context.Context, slugOrID string, byID bool) (PublicPost, error)
	// ListPosts lists one page of public Posts and counts every match.
	ListPosts(ctx context.Context, query ListQuery) ([]post.Post, int64, error)
	// ListMapFeatures lists the mapped public Posts inside the viewport
	// bounds that the filters select, in sort order.
	ListMapFeatures(ctx context.Context, query MapQuery) ([]MapFeatureRow, error)
	// SearchPosts lists up to limit public Posts whose source title or text
	// contains pattern, newest first.
	SearchPosts(ctx context.Context, pattern string, limit int) ([]post.Post, error)
	// ShareLink validates a draft Post's share link; one that does not admit
	// the read is ErrShareLinkNotFound.
	ShareLink(ctx context.Context, token, password, postID string) (*mediaasset.ContentDownloadShareLinkWitness, error)
	// ResolveLocalization picks the displayed locale of the Post in the
	// snapshot, and ResolveOgConsistency its OG image.
	ResolveLocalization(ctx context.Context, postID, acceptLanguage string) (LocalizedContentSelection, error)
	ResolveOgConsistency(ctx context.Context, postID string, selection LocalizedContentSelection) (LocalizedContentSelection, error)
	// ReadyOgAsset resolves the first ready public asset among candidates.
	ReadyOgAsset(ctx context.Context, candidates ...*string) (*commonv1.AssetRef, error)
}

// ErrShareLinkNotFound is a share link that does not admit a draft read.
var ErrShareLinkNotFound = errors.New("post share link not found")

// PublicPost is a Post a public read locked, with its Map Place projection.
type PublicPost struct {
	post.Post
	MapPlace *openv1.MapPlaceBasic
}

// ListQuery selects one page of public Posts.
type ListQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// MapQuery selects the mapped public Posts of a viewport.
type MapQuery struct {
	South, North  float64
	West, East    float64
	FullLongitude bool
	Filters       []*commonv1.FilterSpec
	Sorts         []*commonv1.SortSpec
}

// MapFeatureRow is one mapped Post and its place.
type MapFeatureRow struct {
	PostID       string
	PostTitle    string
	PostSlug     *string
	PlaceID      string
	PlaceName    string
	PlaceAddress string
	PlaceLat     float64
	PlaceLng     float64
}
