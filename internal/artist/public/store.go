package public

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Store is what the public Artist reads read, bound to one read snapshot.
// artistpublicpostgres implements it. A missing Artist is
// artist.ErrArtistNotFound.
type Store interface {
	// ListPublishedArtists lists one page of the published Artists page
	// names (a label_id filter keeps the Artists linked to those Labels)
	// and counts every match.
	ListPublishedArtists(ctx context.Context, page ListPage) ([]artist.Artist, int64, error)
	// FindArtist reads the Artist by ID when byID is set, else by slug.
	FindArtist(ctx context.Context, slugOrID string, byID bool) (artist.Artist, error)
	// ShareLockArtist share-locks the Artist row and reads it.
	ShareLockArtist(ctx context.Context, artistID string) (artist.Artist, error)
	// SourceTitles maps each Artist with a source-locale title to it;
	// SourceOgAssetIDs each to its source-locale OG image.
	SourceTitles(ctx context.Context, artistIDs []string) (map[string]string, error)
	SourceOgAssetIDs(ctx context.Context, artistIDs []string) (map[string]*string, error)
	// ResolveLocalization picks the displayed locale for acceptLanguage, and
	// ResolveOGConsistency keeps the localized OG image only when ready.
	ResolveLocalization(ctx context.Context, artistID, acceptLanguage string) (publiccontent.Selection, error)
	ResolveOGConsistency(ctx context.Context, artistID string, selection publiccontent.Selection, ready publiccontent.ReadyAsset) (publiccontent.Selection, error)
	// Document reads the Artist's content document in locale.
	Document(ctx context.Context, artistID, locale string) (artist.PublicCreativeContentDocument, error)
	// IsGroup reports whether another Artist names the Artist its parent.
	IsGroup(ctx context.Context, artistID string) (bool, error)
	// PublishedReleaseCount and VisibleWorkCount count the Artist's
	// published Releases and its published or archived Works.
	PublishedReleaseCount(ctx context.Context, artistID string) (int64, error)
	VisibleWorkCount(ctx context.Context, artistID string) (int64, error)
	// PublishedLabels lists the first 12 published Labels of the Artist by
	// source title.
	PublishedLabels(ctx context.Context, artistID string) ([]LabelRow, error)
	// PublishedReleases lists one page of the Artist's published Releases
	// in sorts order; ReleaseArtists the other Artists of releaseIDs.
	PublishedReleases(ctx context.Context, artistID string, sorts []Sort, limit, offset int32) ([]ReleaseRow, error)
	ReleaseArtists(ctx context.Context, releaseIDs []string, excludeArtistID string) ([]ReleaseArtistRow, error)
	// VisibleWorks lists one page of the Artist's published or archived
	// Works in sorts order; WorkLocalizations picks each Work's displayed
	// locale and may store the default translation settings, so it needs a
	// writable snapshot (ReadLocked). ResolveLocalization likewise.
	VisibleWorks(ctx context.Context, artistID string, sorts []Sort, limit, offset int32) ([]WorkRow, error)
	WorkLocalizations(ctx context.Context, workIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error)
	// ShareLink reads the live share link of token for the Artist and its
	// password hash.
	ShareLink(ctx context.Context, token, artistID string, now time.Time) (passwordHash *string, found bool, err error)
}

// ListPage is one page of published Artists.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// Sort orders a page by Field.
type Sort struct {
	Field      string
	Descending bool
}

// LabelRow is a published Label of an Artist and its light logo file.
type LabelRow struct {
	ID          string
	Name        string
	Slug        *string
	ImageFileID *string
}

// ReleaseRow is a published Release of an Artist.
type ReleaseRow struct {
	ID          string
	Title       string
	Slug        *string
	Type        string
	ReleaseDate *time.Time
	PublishedAt *time.Time
}

// ReleaseArtistRow is another Artist credited on a Release.
type ReleaseArtistRow struct {
	ReleaseID  string
	ArtistID   string
	ArtistName string
	ArtistSlug *string
}

// WorkRow is a published or archived Work of an Artist.
type WorkRow struct {
	ID          string
	Title       string
	Slug        *string
	Summary     *string
	Type        string
	ImageFileID *string
	PublishedAt *time.Time
}
