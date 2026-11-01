package series

import (
	"errors"
	"time"
)

// Status is the stored lifecycle state of a Post Series. Its values are the
// strings the series table holds.
type Status string

const (
	StatusDraft     Status = "SERIES_STATUS_DRAFT"
	StatusPublished Status = "SERIES_STATUS_PUBLISHED"
)

// Series is a Post Series root with its source-locale copy projected onto
// Title, Description and OgAssetID once the copy is overlaid.
type Series struct {
	ID                  string
	ContentDocumentID   string
	Slug                string
	Status              Status
	SourceLocale        string
	FeaturedImageFileID *string
	CreatedAt           time.Time
	UpdatedAt           *time.Time

	Title       string
	Description *string
	OgAssetID   *string
}

// SourceCopy is the source-owned Series copy used for projection.
type SourceCopy struct {
	Title       *string
	Summary     *string
	ContentText *string
	OgAssetID   *string
}

// Translation is one locale row of a Series: its source copy or a target
// locale copy.
type Translation struct {
	SeriesID    string
	Locale      string
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	OgAssetID   *string
}

// TranslationWrite is the full locale copy an upsert stores.
type TranslationWrite struct {
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	Now         time.Time
}

// ContentDocumentRoot is the Series side of its content document: the
// source locale and the document it points at, if any.
type ContentDocumentRoot struct {
	SourceLocale      string
	ContentDocumentID *string
}

// ContentDocumentRow is the shared content document of a Series.
type ContentDocumentRow struct {
	Profile  string
	Revision *string
}

// ManagerAttribution is the durable product record that a Member manages a
// Series. SpiceDB owns the independent current manager grant.
type ManagerAttribution struct {
	MemberID  string
	CreatedAt time.Time
}

// ManagerSummary is the Member summary a Series manager projection shows.
type ManagerSummary struct {
	MemberID string
	Nickname string
	Avatar   *Asset
}

// Manager is a Series manager as the management API lists it.
type Manager struct {
	MemberID  string
	Nickname  string
	Avatar    *Asset
	CreatedAt time.Time
}

// SeriesPost is a Post of a Series with its own source locale.
type SeriesPost struct {
	ID           string
	Title        string
	SourceLocale string
	Slug         *string
	Status       string
	SeriesOrder  *int
	PublishedAt  *time.Time
}

// AssetDisposition is the content disposition of a public asset, numbered as
// the API's AssetDisposition.
type AssetDisposition int32

// Asset is a ready public asset as the media runtime projects it.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}

// Store errors the use cases branch on.
var (
	ErrSeriesNotFound                 = errors.New("series not found")
	ErrSlugTaken                      = errors.New("series slug is taken")
	ErrContentDocumentNotFound        = errors.New("series content document not found")
	ErrContentDocumentRevisionChanged = errors.New("series content document revision changed")
	ErrTranslationNotFound            = errors.New("series translation not found")
	ErrPostNotFound                   = errors.New("post not found")
	ErrFileNotFound                   = errors.New("file not found")
	// ErrPostRelationChanged reports a Post that left the Series it was
	// read in.
	ErrPostRelationChanged = errors.New("post series relation changed")
)
