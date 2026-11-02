package public

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
)

// ErrMapPlaceNotFound is a missing Map Place of a public event.
var ErrMapPlaceNotFound = errors.New("program event map place not found")

// Store is what the public Program Event, series and type reads read, bound
// to one read snapshot. programeventpublicpostgres implements it. A missing
// event, series or type is programevent.ErrEventNotFound,
// programevent.ErrSeriesNotFound or programevent.ErrTypeNotFound.
type Store interface {
	// PublicEvent share-locks the published or archived event slugOrID
	// names, by ID when byID is set and else by slug.
	PublicEvent(ctx context.Context, slugOrID string, byID bool) (programevent.Event, error)
	// ListPublicEvents lists one page of published and archived events and
	// counts every match. Filters and sorts name the public fields; no sort
	// orders TBA events first, then by start.
	ListPublicEvents(ctx context.Context, page programevent.ListPage) ([]programevent.Event, int64, error)
	// ResolveLocalizations picks each event's displayed locale for
	// acceptLanguage under the stored runtime policy.
	ResolveLocalizations(ctx context.Context, eventIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error)
	// ResolveLocalization picks one event's displayed locale for
	// acceptLanguage under settings; AvailableLocales lists the locales a
	// reader may pick.
	ResolveLocalization(ctx context.Context, eventID, acceptLanguage string, settings translation.RuntimeSettings) (publiccontent.Selection, error)
	AvailableLocales(ctx context.Context, eventID, sourceLocale string, settings translation.RuntimeSettings) ([]string, error)
	// TranslationSettings reads the translation runtime settings, storing
	// the defaults first.
	TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// PosterFileIDs maps each event to its primary poster File;
	// PosterFileID reads one event's, empty when it has none.
	PosterFileIDs(ctx context.Context, eventIDs []string) (map[string]string, error)
	PosterFileID(ctx context.Context, eventID string) (string, error)
	// Types reads the types typeIDs name; TypeLocales their locales, by
	// locale. Type reads one type.
	Types(ctx context.Context, typeIDs []string) (map[string]programevent.Type, error)
	TypeLocales(ctx context.Context, typeIDs []string) (map[string][]programevent.TypeLocale, error)
	Type(ctx context.Context, typeID string) (programevent.Type, error)
	// ListActiveTypes lists one page of active types in their sort order
	// and counts every match.
	ListActiveTypes(ctx context.Context, page programevent.ListPage) ([]programevent.Type, int64, error)
	// MapPlace reads the event's Map Place. A missing place is
	// ErrMapPlaceNotFound.
	MapPlace(ctx context.Context, mapPlaceID string) (MapPlace, error)
	// PublishedSeries reads a published series and reports whether it
	// exists; FindPublishedSeries names it by ID when byID is set and else
	// by slug.
	PublishedSeries(ctx context.Context, seriesID string) (programevent.Series, bool, error)
	FindPublishedSeries(ctx context.Context, slugOrID string, byID bool) (programevent.Series, error)
	// ListPublishedSeries lists one page of published series, most recently
	// updated first, and counts every match.
	ListPublishedSeries(ctx context.Context, page programevent.ListPage) ([]programevent.Series, int64, error)
	// Artists and Labels list the event's published artists and labels,
	// Clients its clients, each by sort order and then name.
	Artists(ctx context.Context, eventID string) ([]Participant, error)
	Labels(ctx context.Context, eventID string) ([]Participant, error)
	Clients(ctx context.Context, eventID string) ([]Client, error)
	// Credits lists the event's credits by sort order and then ID, with the
	// published artist each names and its first image.
	Credits(ctx context.Context, eventID string) ([]CreditRow, error)
	// ContentSnapshot reads the event's content document; MediaReferences
	// its active File usages.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error)
	// ContentFileMIMEType reads the MIME type of the active File a Block
	// reference names, and reports whether exactly one exists.
	ContentFileMIMEType(ctx context.Context, blockID uuid.UUID, referencePath string, fileID uuid.UUID) (string, bool, error)
	// SiteVariables reads the site settings a document's variables render.
	SiteVariables(ctx context.Context) (sitevariable.Context, error)
}

// Units are the read snapshots of the public reads;
// mutation.UnitOfWork[Store] satisfies it.
type Units interface {
	Read(ctx context.Context, read func(Store) error) error
	ReadLocked(ctx context.Context, read func(Store) error) error
	ReadCurrent(ctx context.Context, read func(Store) error) error
}

// MapPlace is the Map Place of a public event.
type MapPlace struct {
	ID            string
	Name          string
	Address       string
	Lat           float64
	Lng           float64
	GooglePlaceID *string
}

// Participant is a published artist or label of an event.
type Participant struct {
	ID        string
	Name      string
	Slug      *string
	Role      *string
	SortOrder int32
}

// Client is a client of an event.
type Client struct {
	ID        string
	Name      string
	Website   *string
	Role      *string
	SortOrder int32
}

// CreditRow is one credit of a public event and the published artist it
// names.
type CreditRow struct {
	ID                string
	DisplayName       *string
	CreditRole        *string
	Description       *string
	SortOrder         int32
	ArtistID          *string
	ArtistName        *string
	ArtistSlug        *string
	ArtistImageFileID *string
	MemberID          *string
}
