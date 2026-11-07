package artist

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status is the stored lifecycle state of an Artist. The values are the
// strings the artist table holds.
type Status string

const (
	StatusDraft     Status = "ARTIST_STATUS_DRAFT"
	StatusPublished Status = "ARTIST_STATUS_PUBLISHED"
)

// Artist is one Artist row.
type Artist struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Slug              *string
	RealName          *string
	ParentArtistID    *string
	CountryCode       *string
	Website           *string
	SocialLinks       map[string]string
	Status            Status
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         *time.Time
}

// LocaleTitle is the title of one stored Artist locale.
type LocaleTitle struct {
	Locale    string
	Title     *string
	UpdatedAt time.Time
}

// DocumentRoot is the Artist row a content document mutation authorizes
// against.
type DocumentRoot struct {
	ID         string
	Status     Status
	DocumentID *uuid.UUID
}

// DocumentMetadata is the Artist metadata a collaboration room edits.
type DocumentMetadata struct {
	RealName    *string
	CountryCode *string
	Website     *string
	SocialLinks map[string]string
	Slug        *string
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
	// ErrArtistNotFound answers a missing Artist row.
	ErrArtistNotFound = errors.New("artist not found")
	// ErrLocaleNotFound answers a missing Artist locale row.
	ErrLocaleNotFound = errors.New("artist locale not found")
	// ErrDocumentRowNotFound answers a missing content document row.
	ErrDocumentRowNotFound = errors.New("artist content document row not found")
)
