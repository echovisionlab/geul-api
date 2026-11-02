package release

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status is the stored lifecycle state of a Release. The values are the
// strings the release table holds.
type Status string

const (
	StatusDraft     Status = "RELEASE_STATUS_DRAFT"
	StatusPublished Status = "RELEASE_STATUS_PUBLISHED"
)

// Type is the stored kind of a Release. The values are the strings the
// release table holds.
type Type string

const (
	TypeAlbum       Type = "RELEASE_TYPE_ALBUM"
	TypeEP          Type = "RELEASE_TYPE_EP"
	TypeSingle      Type = "RELEASE_TYPE_SINGLE"
	TypeCompilation Type = "RELEASE_TYPE_COMPILATION"
)

// Types are the stored Release kinds a filter accepts.
var Types = []Type{TypeAlbum, TypeSingle, TypeEP, TypeCompilation}

// TrackProcessingStatus is the stored audio processing state of a Track.
type TrackProcessingStatus string

const (
	TrackProcessingProcessing TrackProcessingStatus = "TRACK_PROCESSING_STATUS_PROCESSING"
	TrackProcessingCompleted  TrackProcessingStatus = "TRACK_PROCESSING_STATUS_COMPLETED"
)

// DownloadAudienceDisabled is the stored download audience of a Track whose
// original audio may not be downloaded.
const DownloadAudienceDisabled = "disabled"

// Release is one Release row.
type Release struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Slug              *string
	Type              Type
	CatalogNumber     *string
	ReleaseDate       *time.Time
	SpotifyURL        *string
	AppleMusicURL     *string
	BandcampURL       *string
	YoutubeMusicURL   *string
	OgAssetID         *string
	Status            Status
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Settings are the Release columns an update sets; a nil field is left
// alone, and ReleaseDateSet with a nil ReleaseDate clears the date.
type Settings struct {
	Slug            *string
	Type            *Type
	ReleaseDateSet  bool
	ReleaseDate     *time.Time
	SpotifyURL      *string
	AppleMusicURL   *string
	BandcampURL     *string
	YoutubeMusicURL *string
	CatalogNumber   *string
}

// DocumentRoot is the Release row an AI document reads and edits.
type DocumentRoot struct {
	ID           string
	Status       Status
	SourceLocale string
	DocumentID   *uuid.UUID
}

// LocaleMetadata is one stored Release locale.
type LocaleMetadata struct {
	Locale    string
	Title     *string
	UpdatedAt time.Time
}

// Track is one Track row.
type Track struct {
	ID                  string
	ReleaseID           string
	TrackNumber         int
	Title               string
	DurationSeconds     *int
	ProcessingStatus    *string
	Lyrics              *string
	Metadata            map[string]any
	CreatedAt           time.Time
	AudioOriginalFileID *string
	DownloadAudience    string
}

// TrackAudio is the original audio a File ingest attaches to a Track.
// DurationSeconds is stored only when Completed.
type TrackAudio struct {
	FileID          string
	Completed       bool
	DurationSeconds *int
}

// TrackCredit is one track_credit row.
type TrackCredit struct {
	ID           string
	TrackID      string
	ArtistID     *string
	MemberID     *string
	CreditedName *string
	CreditRole   *string
	SortOrder    int
	CreatedAt    time.Time
}

// TrackCreditChange is one credit a Track credit write removed, changed or
// added.
type TrackCreditChange struct {
	CreditID string
	Kind     ChangeKind
}

// ChangeKind is how a write changed one relation row.
type ChangeKind uint8

const (
	ChangeCreated ChangeKind = iota + 1
	ChangeUpdated
	ChangeDeleted
)

// CreditRow is a stored Track credit with the kind of its attribution.
type CreditRow struct {
	ID           string
	CreditType   string
	ArtistID     *string
	MemberID     *string
	CreditedName *string
	CreditRole   *string
	SortOrder    int
}

// ReleaseArtist is one Artist of a Release.
type ReleaseArtist struct {
	ArtistID  string
	SortOrder int
}

// ReleaseLabel is one Label of a Release.
type ReleaseLabel struct {
	LabelID       string
	CatalogNumber *string
	SortOrder     int
}

// ReleaseFormat is one Format of a Release.
type ReleaseFormat struct {
	FormatID          string
	FormatDescription *string
}

// ReleaseCredit is one release_credit row.
type ReleaseCredit struct {
	ID           string
	ArtistID     *string
	MemberID     *string
	CreditedName *string
	CreditRole   *string
	SortOrder    int
}

// ReferenceRelation is a Release relation to a catalog reference by ID.
type ReferenceRelation uint8

const (
	ReferenceCategories ReferenceRelation = iota + 1
	ReferenceGenres
	ReferenceStyles
)

// Reference is a catalog reference a Release links to.
type Reference struct {
	ID   string
	Name string
	Slug string
}

// FormatReference is a Format a Release links to, with the Release's
// description of it.
type FormatReference struct {
	Reference
	Description *string
}

// EditorCredit is a Release credit as the relation editor reads it.
type EditorCredit struct {
	ID           string
	ArtistID     *string
	MemberID     *string
	MemberName   *string
	CreditedName *string
	CreditRole   *string
	SortOrder    int32
}

// Summary is the title and slug of another domain's entity a Release
// references.
type Summary struct {
	Title string
	Slug  string
}

// ArtistSummary and LabelSummary are the summaries of the Artists and Labels
// a Release references.
type (
	ArtistSummary = Summary
	LabelSummary  = Summary
)

// MemberReference is a credited Member, which must stay a live Member while
// its credit is written; Field names it in an answer.
type MemberReference struct {
	MemberID string
	Field    string
}

// RowLock is the row lock a Store read takes.
type RowLock uint8

const (
	// Unlocked reads without a row lock.
	Unlocked RowLock = iota
	// KeyShareLock blocks a concurrent delete or key change.
	KeyShareLock
	// ShareLock blocks a concurrent update.
	ShareLock
	// UpdateLock serializes with a concurrent writer.
	UpdateLock
)

// Sentinel answers of a Store.
var (
	// ErrReleaseNotFound answers a missing Release row.
	ErrReleaseNotFound = errors.New("release not found")
	// ErrSlugTaken answers a write whose slug another Release holds.
	ErrSlugTaken = errors.New("release slug taken")
	// ErrLocaleNotFound answers a missing Release locale row.
	ErrLocaleNotFound = errors.New("release locale not found")
	// ErrTrackNotFound answers a missing Track row.
	ErrTrackNotFound = errors.New("track not found")
	// ErrFileNotFound answers a missing File row.
	ErrFileNotFound = errors.New("file not found")
)
