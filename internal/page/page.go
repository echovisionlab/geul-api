// Package page owns Page-specific domain behavior.
package page

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
)

// Status is the stored lifecycle state of a Page. The values are the
// strings the page table holds.
type Status string

const (
	StatusDraft     Status = "PAGE_STATUS_DRAFT"
	StatusPublished Status = "PAGE_STATUS_PUBLISHED"
)

// Page is one Page row. AccessPolicy is the stored audience, which
// pageaccess decodes: a corrupt policy fails only the reads that decode it.
type Page struct {
	ID                  string
	ContentDocumentID   *string
	SourceLocale        string
	Slug                *string
	DocumentLayout      DocumentLayout
	AccessPolicy        json.RawMessage
	Status              Status
	ShowTitle           bool
	FeaturedImageFileID *string
	OgAssetID           *string
	PublishedAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ContentHeight is how tall a Page document renders. The values are the
// strings document_layout holds.
type ContentHeight string

const (
	ContentHeightContent  ContentHeight = "content"
	ContentHeightViewport ContentHeight = "viewport"
)

// RegionPlacement is where the page chrome or footer sits. The values are
// the strings document_layout holds.
type RegionPlacement string

const (
	PlacementFlow   RegionPlacement = "flow"
	PlacementPinned RegionPlacement = "pinned"
)

// DocumentLayout is the layout of a Page document.
type DocumentLayout struct {
	ContentHeight       ContentHeight
	PageChrome          RegionPlacement
	Footer              RegionPlacement
	HideTableOfContents bool
}

// DefaultDocumentLayout is the layout of a new Page.
func DefaultDocumentLayout() DocumentLayout {
	return DocumentLayout{ContentHeight: ContentHeightContent, PageChrome: PlacementFlow, Footer: PlacementFlow}
}

// Validate rejects a layout value the API cannot render.
func (l DocumentLayout) Validate() error {
	switch l.ContentHeight {
	case ContentHeightContent, ContentHeightViewport:
	default:
		return fmt.Errorf("invalid contentHeight %q", l.ContentHeight)
	}
	switch l.PageChrome {
	case PlacementFlow, PlacementPinned:
	default:
		return fmt.Errorf("invalid pageChrome %q", l.PageChrome)
	}
	switch l.Footer {
	case PlacementFlow, PlacementPinned:
	default:
		return fmt.Errorf("invalid footer %q", l.Footer)
	}
	return nil
}

// Settings is the locked Page configuration an update or deletion reads.
type Settings struct {
	Slug              *string
	ShowTitle         bool
	ContentDocumentID *uuid.UUID
	UpdatedAt         time.Time
	AccessPolicy      json.RawMessage
}

// SettingsChange is the Page configuration an update writes. SetSlug with a
// nil Slug clears the slug; a nil ShowTitle or AccessPolicy leaves it.
type SettingsChange struct {
	SetSlug      bool
	Slug         *string
	ShowTitle    *bool
	AccessPolicy json.RawMessage
}

// DocumentRoot is the content document reference and source locale of a
// Page row.
type DocumentRoot struct {
	ID                string
	ContentDocumentID *uuid.UUID
	SourceLocale      string
}

// SourceMetadata is the title, summary and OG image of a Page's source
// locale. Block content and revisions live in the content document.
type SourceMetadata struct {
	Locale    string
	Title     *string
	Summary   *string
	OgAssetID *string
}

// GetLocale returns the locale of metadata, or empty for nil.
func (m *SourceMetadata) GetLocale() string {
	if m == nil {
		return ""
	}
	return m.Locale
}

// Locale is one stored Page locale: its title and summary, and the
// incarnation and time that derive its target revision.
type Locale struct {
	Locale        string
	Title         *string
	Summary       *string
	IncarnationID uuid.UUID
	UpdatedAt     time.Time
}

// LocaleChange is a write to one Page locale row. SetTitle and SetSummary
// select the columns it writes; UpdatedAt is always written.
type LocaleChange struct {
	SetTitle   bool
	Title      *string
	SetSummary bool
	Summary    *string
	UpdatedAt  time.Time
}

// Version is one Page version checkpoint.
type Version struct {
	ID                   string
	PageID               string
	Version              int32
	Title                *string
	Summary              *string
	ContentSnapshot      json.RawMessage
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

// Locator names the column a Page read matches.
type Locator uint8

const (
	ByID Locator = iota + 1
	BySlug
)

// ListPage is the page of Pages a Store lists: filtered, sorted and paged.
type ListPage struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// TranslationJob is the translation job a delivery applies to a Page.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// Sentinel answers of a Store.
var (
	// ErrPageNotFound answers a missing Page row.
	ErrPageNotFound = errors.New("page not found")
	// ErrSlugTaken answers a Page write whose slug another Page holds.
	ErrSlugTaken = errors.New("page slug taken")
	// ErrVersionNotFound answers a missing Page version.
	ErrVersionNotFound = errors.New("page version not found")
	// ErrFileNotFound answers a missing File.
	ErrFileNotFound = errors.New("file not found")
)

// RequireDocumentLayout requires a requested layout whose values the API
// names: the documentlayout.FromProto contract over the domain values.
func RequireDocumentLayout(layout *DocumentLayout) (DocumentLayout, error) {
	if layout == nil {
		return DocumentLayout{}, fmt.Errorf("document layout is required")
	}
	switch layout.ContentHeight {
	case ContentHeightContent, ContentHeightViewport:
	default:
		return DocumentLayout{}, fmt.Errorf("content_height must be CONTENT or VIEWPORT")
	}
	switch layout.PageChrome {
	case PlacementFlow, PlacementPinned:
	default:
		return DocumentLayout{}, fmt.Errorf("page_chrome must be FLOW or PINNED")
	}
	switch layout.Footer {
	case PlacementFlow, PlacementPinned:
	default:
		return DocumentLayout{}, fmt.Errorf("footer must be FLOW or PINNED")
	}
	return *layout, nil
}
