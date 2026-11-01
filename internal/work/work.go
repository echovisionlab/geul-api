package work

import (
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// Status is the stored lifecycle state of a Work. The values are the
// strings the work table holds.
type Status string

const (
	StatusDraft     Status = "WORK_STATUS_DRAFT"
	StatusPublished Status = "WORK_STATUS_PUBLISHED"
	// StatusArchived is a legacy state; publishing restores it.
	StatusArchived Status = "WORK_STATUS_ARCHIVED"
)

// Statuses are the stored statuses, for filters over work.status.
func Statuses() []string {
	return []string{string(StatusDraft), string(StatusPublished), string(StatusArchived)}
}

// Work is one Work row.
type Work struct {
	ID                  string
	ContentDocumentID   *string
	SourceLocale        string
	Slug                *string
	Type                string
	Year                int32
	Month               int32
	UntilYear           *int32
	UntilMonth          *int32
	IsPresent           bool
	MapPlaceID          *string
	FeaturedImageFileID *string
	Metadata            structured.Fields
	Featured            bool
	Status              Status
	CreatedAt           time.Time
	UpdatedAt           time.Time
	PublishedAt         *time.Time
}

// Root is the Work row a content mutation or an authorization check locks.
type Root struct {
	ID                string
	Status            Status
	ContentDocumentID *string
	SourceLocale      string
}

// LocaleMetadata is one work_translation row: a locale's title and summary.
type LocaleMetadata struct {
	Locale    string
	Title     *string
	Summary   *string
	UpdatedAt time.Time
}

// SourceDocument is the source-locale title, summary and OG image of a Work.
type SourceDocument struct {
	Title     *string
	Summary   *string
	OgAssetID *string
}

// CreditGroup is one work_credit_group row.
type CreditGroup struct {
	ID        string
	WorkID    string
	Name      string
	SortOrder int
}

// Credit is one work_credit row: an Artist, a Member or a plain name.
type Credit struct {
	ID         string
	WorkID     string
	GroupID    *string
	ArtistID   *string
	MemberID   *string
	Name       *string
	CreditRole *string
	SortOrder  int
}

// CreditItemKind names an item of the credit order. The values are the API
// enum's, so a value it does not know stays unknown.
type CreditItemKind int32

const (
	CreditItemGroup  CreditItemKind = 1
	CreditItemCredit CreditItemKind = 2
)

// CreditArtistRow is a credited Artist with its first image file.
type CreditArtistRow struct {
	ID          string
	Name        string
	Slug        *string
	ImageFileID *string
}

// ClientRow is a client a Work lists, with its logo files.
type ClientRow struct {
	ID          string
	Name        string
	Website     *string
	LightFileID *string
	DarkFileID  *string
}

// Version is one work_version row.
type Version struct {
	ID                   string
	WorkID               string
	Version              int32
	Title                *string
	Summary              *string
	ContentSnapshot      []byte
	ContributorMemberIDs []string
	CreatedAt            time.Time
}

// RowLock is the row lock a Store read takes.
type RowLock uint8

const (
	// Unlocked reads without a row lock.
	Unlocked RowLock = iota
	// ShareLock blocks a concurrent update.
	ShareLock
	// UpdateLock serializes with a concurrent writer.
	UpdateLock
)

// Sentinel answers of a Store.
var (
	// ErrWorkNotFound answers a missing Work row.
	ErrWorkNotFound = errors.New("work not found")
	// ErrCreditGroupNotFound answers a missing credit group row.
	ErrCreditGroupNotFound = errors.New("work credit group not found")
	// ErrCreditNotFound answers a missing credit row.
	ErrCreditNotFound = errors.New("work credit not found")
	// ErrVersionNotFound answers a missing version row.
	ErrVersionNotFound = errors.New("work version not found")
	// ErrFileNotFound answers a missing File row.
	ErrFileNotFound = errors.New("work file not found")
	// ErrLocaleNotFound answers a missing work_translation row.
	ErrLocaleNotFound = errors.New("work locale not found")
	// ErrSlugTaken answers a slug another Work holds (the unique index).
	ErrSlugTaken = errors.New("work slug taken")
	// ErrRowChanged answers a write that matched no row.
	ErrRowChanged = errors.New("work row changed")
)
