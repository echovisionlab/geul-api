package programevent

import (
	"time"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Status is the stored lifecycle state of a Program Event. A series stores
// the draft and published values of the same type. The values are the
// strings the program_event and program_event_series tables hold.
type Status string

const (
	StatusDraft     Status = "PROGRAM_EVENT_STATUS_DRAFT"
	StatusPublished Status = "PROGRAM_EVENT_STATUS_PUBLISHED"
	StatusArchived  Status = "PROGRAM_EVENT_STATUS_ARCHIVED"
)

// TypeStatus is the stored availability of a Program Event type.
type TypeStatus string

const (
	TypeStatusActive   TypeStatus = "PROGRAM_EVENT_TYPE_STATUS_ACTIVE"
	TypeStatusInactive TypeStatus = "PROGRAM_EVENT_TYPE_STATUS_INACTIVE"
)

// LocationMode is where a Program Event takes place, as program_event
// stores it.
type LocationMode string

const (
	LocationModeMapPlace LocationMode = "PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE"
	LocationModeOnline   LocationMode = "PROGRAM_EVENT_LOCATION_MODE_ONLINE"
	LocationModeHybrid   LocationMode = "PROGRAM_EVENT_LOCATION_MODE_HYBRID"
	LocationModeTBA      LocationMode = "PROGRAM_EVENT_LOCATION_MODE_TBA"
)

// UsesMapPlace reports whether the mode names a Map Place.
func (mode LocationMode) UsesMapPlace() bool {
	return mode == LocationModeMapPlace || mode == LocationModeHybrid
}

// Event is one stored Program Event.
type Event struct {
	ID                string
	ContentDocumentID *string
	Title             string
	Slug              string
	Status            Status
	SourceLocale      string
	TypeID            string
	SeriesID          *string
	SeriesOrder       *int32
	StartsAt          time.Time
	EndsAt            *time.Time
	Timezone          string
	AllDay            bool
	LocationMode      LocationMode
	MapPlaceID        *string
	TicketURL         *string
	StreamURL         *string
	ExternalURL       *string
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Lifecycle is the publication state of an event.
type Lifecycle struct {
	ID          string
	Status      Status
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// DocumentRoot is the event row an AI document reads: its identity, title,
// lifecycle and content document.
type DocumentRoot struct {
	ID                string
	Title             string
	ContentDocumentID *string
	Status            Status
	SourceLocale      string
}

// LocaleMetadata is one program_event_translation row.
type LocaleMetadata struct {
	Locale    string
	Summary   *string
	UpdatedAt time.Time
}

// LocaleSummary is the summary of one locale of an event.
type LocaleSummary struct {
	Locale  string
	Summary *string
}

// Media is one media relation of an event.
type Media struct {
	ID        string
	EventID   string
	FileID    string
	Role      string
	SortOrder int32
	IsPrimary bool
	Alt       *string
	Caption   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreditRecord is one stored credit of an event.
type CreditRecord struct {
	ID          string
	EventID     string
	ArtistID    *string
	MemberID    *string
	DisplayName *string
	CreditRole  *string
	Description *string
	SortOrder   int32
	CreatedAt   time.Time
}

// CreditArtist is the credited artist of a credit, with its first image.
type CreditArtist struct {
	ID         string
	Name       string
	Slug       *string
	ImageAsset *mediaasset.Reference
}

// CreditView is a credit with the projections of its credited artist or
// Member.
type CreditView struct {
	CreditRecord
	Artist *CreditArtist
	Member *memberref.Summary
}

// RelationKind names the artists, labels or clients of an event.
type RelationKind uint8

const (
	ArtistRelation RelationKind = iota + 1
	LabelRelation
	ClientRelation
)

// Series is one stored Program Event series.
type Series struct {
	ID           string
	Slug         string
	Status       Status
	Title        string
	Summary      *string
	Description  *string
	PosterFileID *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Type is one stored Program Event type.
type Type struct {
	ID                string
	Slug              string
	Status            TypeStatus
	SortOrder         int32
	RequiresPlace     bool
	RequiresStreamURL bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TypeLocale is the name of a type in one locale.
type TypeLocale struct {
	TypeID      string
	Locale      string
	Name        string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TypeView is a type with every locale, ordered by locale.
type TypeView struct {
	Type
	Locales []TypeLocale
}

// LocaleView is one locale of an event's administrator view: its summary
// and the rendered document.
type LocaleView struct {
	Locale      string
	Summary     *string
	ContentHTML string
	ContentText string
}

// EventView is the administrator view of an event.
type EventView struct {
	Event
	// PosterFileID is the primary poster.
	PosterFileID     *string
	Locales          []LocaleView
	Artists          []Relation
	Labels           []Relation
	Clients          []Relation
	Credits          []CreditView
	Media            []Media
	Document         *contentv1.RichTextDocument
	DocumentRevision string
	BlockMedia       []*contentv1.ContentBlockMediaItem
}

// EventSummary is an event in the administrator list, with its primary
// poster.
type EventSummary struct {
	Event
	PosterFileID *string
}

// Pagination is where a page sits among every match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}
