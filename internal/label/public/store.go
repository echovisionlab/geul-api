package public

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/label"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

// Store is what the public Label reads read, bound to one read snapshot.
// labelpublicpostgres implements it. A missing Label is
// label.ErrLabelNotFound.
type Store interface {
	// ListPublishedLabels lists one page of the Labels page names and
	// counts every match.
	ListPublishedLabels(ctx context.Context, page label.ListPage) ([]label.Label, int64, error)
	// FindLabel reads the Label by ID when byID is set, else by slug.
	FindLabel(ctx context.Context, slugOrID string, byID bool) (label.Label, error)
	// ShareLockLabel share-locks the Label row and reads it.
	ShareLockLabel(ctx context.Context, labelID string) (label.Label, error)
	// ResolveLocalization picks the displayed locale for acceptLanguage.
	ResolveLocalization(ctx context.Context, labelID, acceptLanguage string) (publiccontent.Selection, error)
	// SourceTitles maps each Label to its source-locale title.
	SourceTitles(ctx context.Context, labelIDs []string) (map[string]string, error)
	// Document reads the Label's content document in locale.
	Document(ctx context.Context, labelID, locale string) (ContentDocument, error)
	// ReadySourceFiles resolves ready assets of kinds by source File ID,
	// ReadyAssets by asset ID; ResolveOg resolves one OG image.
	ReadySourceFiles(ctx context.Context, fileIDs []string, kinds ...string) (map[string]*mediaasset.Reference, error)
	ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error)
	ResolveOg(ctx context.Context, assetID *string) (*mediaasset.Reference, error)
	// RelationCounts counts the Label's published Releases and Artists.
	RelationCounts(ctx context.Context, labelID string) (releases, artists int32, err error)
	// PublishedParent reads the parent Label when it is published.
	PublishedParent(ctx context.Context, labelID string) (ParentRow, bool, error)
	// PublishedArtists lists the first 12 published Artists of the Label by
	// source title.
	PublishedArtists(ctx context.Context, labelID string) ([]ArtistRow, error)
	// PublishedReleases lists the first 12 published Releases of the Label,
	// newest release date first.
	PublishedReleases(ctx context.Context, labelID string) ([]ReleaseRow, error)
	// ReleaseArtists lists the Artists of each Release in credit order.
	ReleaseArtists(ctx context.Context, releaseIDs []string) ([]ReleaseArtistRow, error)
}

// ParentRow is a published parent Label.
type ParentRow struct {
	ID              string
	Name            string
	Slug            *string
	LogoLightFileID *string
	LogoDarkFileID  *string
	OgAssetID       *string
}

// ArtistRow is a published Artist of a Label and its first image.
type ArtistRow struct {
	ID          string
	Name        string
	Slug        *string
	ImageFileID *string
}

// ReleaseRow is a published Release of a Label and its first artwork.
type ReleaseRow struct {
	ID            string
	Title         string
	Slug          *string
	Type          string
	ReleaseDate   *time.Time
	PublishedAt   *time.Time
	ArtworkFileID *string
}

// ReleaseArtistRow is an Artist credited on a Release.
type ReleaseArtistRow struct {
	ReleaseID  string
	ArtistID   string
	ArtistName string
	ArtistSlug *string
}
