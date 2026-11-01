package label

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status is the stored lifecycle state of a Label. The values are the
// strings the label table holds.
type Status string

const (
	StatusDraft     Status = "LABEL_STATUS_DRAFT"
	StatusPublished Status = "LABEL_STATUS_PUBLISHED"
)

// Label is one Label row.
type Label struct {
	ID                string
	ContentDocumentID *string
	SourceLocale      string
	Slug              *string
	CountryCode       *string
	Website           *string
	LogoLightFileID   *string
	LogoDarkFileID    *string
	SocialLinks       map[string]string
	ParentLabelID     *string
	OgAssetID         *string
	Status            Status
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// LocaleTitle is the title of one stored Label locale.
type LocaleTitle struct {
	Locale    string
	Title     *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DocumentRoot is the Label row a content document mutation authorizes
// against.
type DocumentRoot struct {
	ID         string
	Status     Status
	DocumentID *uuid.UUID
}

// DocumentMetadata is the Label metadata a collaboration room edits.
type DocumentMetadata struct {
	Slug          *string
	CountryCode   *string
	Website       *string
	SocialLinks   map[string]string
	ParentLabelID *string
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

// DeletionImpactKind names what references a Label a deletion detaches.
// The values are those of managev1.LabelDeletionImpactKind.
type DeletionImpactKind int32

const (
	ImpactChildLabel   DeletionImpactKind = 1
	ImpactArtist       DeletionImpactKind = 2
	ImpactProgramEvent DeletionImpactKind = 3
	ImpactRelease      DeletionImpactKind = 4
)

// DeletionImpact is one row that references the Label.
type DeletionImpact struct {
	Kind        DeletionImpactKind
	EntityID    string
	DisplayName string
}

// Sentinel answers of a Store.
var (
	// ErrLabelNotFound answers a missing Label row.
	ErrLabelNotFound = errors.New("label not found")
	// ErrSlugTaken answers an insert whose slug another Label holds.
	ErrSlugTaken = errors.New("label slug taken")
	// ErrLocaleNotFound answers a missing Label locale row.
	ErrLocaleNotFound = errors.New("label locale not found")
)
