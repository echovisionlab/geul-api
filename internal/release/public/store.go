package public

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Store is what the public Release reads read, bound to one read snapshot.
// releasepublicpostgres implements it. A missing Release is
// release.ErrReleaseNotFound.
type Store interface {
	// FindRelease reads the Release by ID when byID is set, else by slug.
	FindRelease(ctx context.Context, slugOrID string, byID bool) (release.Release, error)
	// FindShareLink reads the share link of token for the Release that has
	// not expired at now; none is ErrShareLinkNotFound.
	FindShareLink(ctx context.Context, token, releaseID string, now time.Time) (ShareLink, error)
	// ListPublishedReleases lists one page of published Releases and counts
	// every match.
	ListPublishedReleases(ctx context.Context, page ListPage) ([]release.Release, int64, error)
	// SourceTitles maps each Release that has one to its source-locale
	// title.
	SourceTitles(ctx context.Context, releaseIDs []string) (map[string]string, error)
	// ResolveLocalization picks the displayed locale for acceptLanguage,
	// storing default settings when there are none.
	ResolveLocalization(ctx context.Context, releaseID, acceptLanguage string) (publiccontent.Selection, error)
	// ContentDocument share-locks the Release root and reads its content
	// document in locale.
	ContentDocument(ctx context.Context, releaseID, locale string) (release.PublicContentDocument, error)
	// ArtworkFiles lists each Release's first artwork File by sort order,
	// then creation.
	ArtworkFiles(ctx context.Context, releaseIDs []string) ([]ArtworkFile, error)
	// ReadySourceAssets resolves ready public assets of kinds by source File
	// ID.
	ReadySourceAssets(ctx context.Context, fileIDs []string, kinds ...string) (map[string]*commonv1.AssetRef, error)
	// ReleaseArtists lists each Release's Artists with their first image by
	// Release, sort order and creation.
	ReleaseArtists(ctx context.Context, releaseIDs []string) ([]ArtistRow, error)
	// ArtistSummaries and LabelSummaries read the summaries of the Artists
	// and Labels that exist among ids.
	ArtistSummaries(ctx context.Context, artistIDs []string) (map[string]release.ArtistSummary, error)
	LabelSummaries(ctx context.Context, labelIDs []string) (map[string]release.LabelSummary, error)
	// ReleaseLabels lists the Release's Labels by sort order.
	ReleaseLabels(ctx context.Context, releaseID string) ([]LabelRow, error)
	// Genres, Styles and Formats list the Release's references by name.
	Genres(ctx context.Context, releaseID string) ([]release.Reference, error)
	Styles(ctx context.Context, releaseID string) ([]release.Reference, error)
	Formats(ctx context.Context, releaseID string) ([]release.FormatReference, error)
	// Credits lists the Release's credits with their Artists' first images
	// by sort order and creation.
	Credits(ctx context.Context, releaseID string) ([]CreditRow, error)
	// CreditNotes maps each credit of the Release with a note in locale to
	// it.
	CreditNotes(ctx context.Context, releaseID, locale string) (map[string]string, error)
	// Tracks lists the Release's Tracks by track number.
	Tracks(ctx context.Context, releaseID string) ([]release.Track, error)
	// TrackFiles reads the live original audio Files among fileIDs that
	// have an extension, lowercased.
	TrackFiles(ctx context.Context, fileIDs []string) (map[string]MediaFile, error)
	// TrackWaveforms, TrackSpectrograms and TrackHLS resolve the ready
	// derivatives of the original audio Files.
	TrackWaveforms(ctx context.Context, fileIDs []string) (map[string]*commonv1.AssetRef, error)
	TrackSpectrograms(ctx context.Context, fileIDs []string) (map[string]*commonv1.AssetRef, error)
	TrackHLS(ctx context.Context, fileIDs []string) (map[string]*commonv1.HlsMediaRef, error)
	// TrackCredits lists the Tracks' credits with their Artists' first
	// images by Track, sort order and creation.
	TrackCredits(ctx context.Context, trackIDs []string) ([]TrackCreditRow, error)
}

// ErrShareLinkNotFound answers a share link that does not exist or expired.
var ErrShareLinkNotFound = errors.New("share link not found")

// ShareLink is a share link of a draft.
type ShareLink struct {
	ID           string
	EntityType   string
	EntityID     string
	PasswordHash *string
	ExpiresAt    *time.Time
}

// ListPage is the page of published Releases a Store lists. RelationFilters
// narrow the Releases to those related to the IDs; Filters are the rest.
type ListPage struct {
	RelationFilters []RelationFilter
	Filters         []*commonv1.FilterSpec
	Sorts           []*commonv1.SortSpec
	Limit           int32
	Offset          int32
}

// RelationFilter keeps the Releases related through Relation to one of IDs.
// One means the filter compared to one value.
type RelationFilter struct {
	Relation Relation
	IDs      []string
	One      bool
}

// Relation is a Release relation a list filters on.
type Relation uint8

const (
	RelationLabel Relation = iota + 1
	RelationArtist
	RelationCategory
)

// ArtworkFile is the first artwork File of a Release.
type ArtworkFile struct {
	ReleaseID string
	FileID    string
}

// ArtistRow is an Artist of a Release and its first image File.
type ArtistRow struct {
	ReleaseID   string
	ArtistID    string
	ImageFileID *string
}

// LabelRow is a Label of a Release.
type LabelRow struct {
	ID            string
	CatalogNumber *string
}

// CreditRow is a credit of a Release and its Artist's first image File.
type CreditRow struct {
	ID           string
	CreditedName *string
	CreditRole   *string
	ArtistID     *string
	MemberID     *string
	ImageFileID  *string
}

// TrackCreditRow is a credit of a Track and its Artist's first image File.
type TrackCreditRow struct {
	TrackID      string
	ID           string
	CreditedName *string
	RoleName     *string
	ArtistID     *string
	ImageFileID  *string
}
