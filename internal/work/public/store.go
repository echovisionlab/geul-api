package public

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
)

// Store is what the public Work reads read, bound to one read snapshot.
// workpublicpostgres implements it. A missing Work is
// workdomain.ErrWorkNotFound.
type Store interface {
	// ListWorks lists one page of the published and archived Works page
	// names and counts every match.
	ListWorks(ctx context.Context, page ListPage) ([]workdomain.Work, int64, error)
	// SourceDocuments maps each Work with a source-locale row to its title,
	// summary and OG image.
	SourceDocuments(ctx context.Context, workIDs []string) (map[string]workdomain.SourceDocument, error)
	// ResolveLocalizations picks each Work's displayed locale for
	// acceptLanguage. It may store the default translation settings, so it
	// needs a writable snapshot (ReadLocked).
	ResolveLocalizations(ctx context.Context, workIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error)
	// MapFeatures lists the published and archived Works mapped inside
	// bounds, filtered and sorted.
	MapFeatures(ctx context.Context, bounds MapBounds, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) ([]MapFeatureRow, error)
	// FindWork share-locks and reads the Work by ID when byID is set, else
	// by slug, with its projected Map Place.
	FindWork(ctx context.Context, slugOrID string, byID bool) (Work, error)
	// ContentDocumentID share-locks the Work and reads its content
	// document.
	ContentDocumentID(ctx context.Context, workID string) (uuid.UUID, error)
	// ResolveLocalization picks the displayed locale for acceptLanguage, and
	// ResolveOGConsistency keeps the localized OG image only when ready.
	ResolveLocalization(ctx context.Context, workID, acceptLanguage string) (publiccontent.Selection, error)
	ResolveOGConsistency(ctx context.Context, workID string, selection publiccontent.Selection) (publiccontent.Selection, error)
	// Document reads the Work's content document in locale with its block
	// media.
	Document(ctx context.Context, workID string, documentID uuid.UUID, locale string) (Document, error)
	// CreditGroups and Clients read in a savepoint, so a failure leaves the
	// snapshot usable; Credits fails the read.
	CreditGroups(ctx context.Context, workID string) ([]workdomain.CreditGroup, error)
	Credits(ctx context.Context, workID string) ([]workdomain.Credit, error)
	Clients(ctx context.Context, workID string) ([]workdomain.ClientRow, error)
	// ShareLink reads the live share link of token for the Work.
	ShareLink(ctx context.Context, token, workID string, now time.Time) (ShareLink, bool, error)
	// CreditArtist reads a credited Artist; ok is false when it is gone.
	CreditArtist(ctx context.Context, artistID string) (CreditArtistRow, bool, error)
}

// ListPage is one page of public Works.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// MapBounds is the box a map read selects; FullLongitude keeps every
// longitude.
type MapBounds struct {
	West, South, East, North float64
	FullLongitude            bool
}

// MapFeatureRow is a mapped Work with its place.
type MapFeatureRow struct {
	WorkID       string
	WorkTitle    string
	WorkSlug     *string
	PlaceID      string
	PlaceName    string
	PlaceAddress string
	PlaceLat     float64
	PlaceLng     float64
}

// Work is a public Work row with its projected Map Place.
type Work struct {
	workdomain.Work
	MapPlace *openv1.MapPlaceBasic
}

// Document is a Work's content document in one locale with its block
// media.
type Document struct {
	Document   *contentv1.LocalizedRichTextDocument
	Revision   string
	BlockMedia []*contentv1.ContentBlockMediaItem
}

// ShareLink is a live share link of a Work.
type ShareLink struct {
	PasswordHash *string
	Witness      *mediaasset.ContentDownloadShareLinkWitness
}

// CreditArtistRow is a credited Artist.
type CreditArtistRow struct {
	ID   string
	Name string
	Slug *string
}
