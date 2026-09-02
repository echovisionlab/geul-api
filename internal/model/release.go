package model

import (
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Release represents the release table
// Editor permissions are managed by SpiceDB.
type Release struct {
	ID                   string                      `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID    *string                     `gorm:"column:content_document_id;type:uuid"`
	SourceLocale         string                      `gorm:"column:source_locale;type:text;not null;default:en"`
	ContentDocument      *contentv1.RichTextDocument `gorm:"-"`
	ContentRevision      string                      `gorm:"-"`
	ContentCanonicalHash string                      `gorm:"-"`
	Title                string                      `gorm:"-"`
	Slug                 *string                     `gorm:"column:slug;type:varchar(255);uniqueIndex"`
	Type                 string                      `gorm:"column:type;type:varchar(50);not null;default:RELEASE_TYPE_ALBUM"`
	Description          *string                     `gorm:"-"`
	DescriptionHTML      *string                     `gorm:"-"`
	DescriptionJSON      []byte                      `gorm:"-"`
	CatalogNumber        *string                     `gorm:"column:catalog_number;type:varchar(100)"`
	ReleaseDate          *time.Time                  `gorm:"column:release_date;type:date"`
	SpotifyURL           *string                     `gorm:"column:spotify_url;type:varchar(500)"`
	AppleMusicURL        *string                     `gorm:"column:apple_music_url;type:varchar(500)"`
	BandcampURL          *string                     `gorm:"column:bandcamp_url;type:varchar(500)"`
	YoutubeMusicURL      *string                     `gorm:"column:youtube_music_url;type:varchar(500)"`
	OgAssetID            *string                     `gorm:"column:og_asset_id;type:uuid"`
	Status               string                      `gorm:"column:status;type:varchar(50);not null;default:RELEASE_STATUS_DRAFT"`
	PublishedAt          *time.Time                  `gorm:"column:published_at"`
	CreatedAt            time.Time                   `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            time.Time                   `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (Release) TableName() string {
	return "release"
}

// ReleaseFile represents the release_file join table (for artwork)
type ReleaseFile struct {
	ReleaseID string    `gorm:"column:release_id;type:uuid;primaryKey"`
	FileID    string    `gorm:"column:file_id;type:uuid;primaryKey"`
	SortOrder int       `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (ReleaseFile) TableName() string {
	return "release_file"
}

// ReleaseCredit represents the release_credit junction table
type ReleaseCredit struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ReleaseID    string    `gorm:"column:release_id;type:uuid;not null"`
	ArtistID     *string   `gorm:"column:artist_id;type:uuid"`
	MemberID     *string   `gorm:"column:member_id;type:uuid"`
	CreditedName *string   `gorm:"column:credited_name;type:varchar(255)"`
	CreditRole   *string   `gorm:"column:credit_role;type:varchar(100)"`
	SortOrder    int       `gorm:"column:sort_order;default:0"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (ReleaseCredit) TableName() string {
	return "release_credit"
}

// ReleaseArtist represents the release_artist junction table.
type ReleaseArtist struct {
	ReleaseID string    `gorm:"column:release_id;type:uuid;primaryKey"`
	ArtistID  string    `gorm:"column:artist_id;type:uuid;primaryKey"`
	SortOrder int       `gorm:"column:sort_order;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM.
func (ReleaseArtist) TableName() string {
	return "release_artist"
}

// ReleaseLabel represents the release_label junction table
type ReleaseLabel struct {
	ReleaseID     string  `gorm:"column:release_id;type:uuid;primaryKey"`
	LabelID       string  `gorm:"column:label_id;type:uuid;primaryKey"`
	CatalogNumber *string `gorm:"column:catalog_number;type:varchar(100)"`
	SortOrder     int     `gorm:"column:sort_order;default:0"`
}

// TableName returns the table name for GORM
func (ReleaseLabel) TableName() string {
	return "release_label"
}

// ReleaseCategory represents the release_category junction table
type ReleaseCategory struct {
	ReleaseID  string `gorm:"column:release_id;type:uuid;primaryKey"`
	CategoryID string `gorm:"column:category_id;type:uuid;primaryKey"`
}

// TableName returns the table name for GORM
func (ReleaseCategory) TableName() string {
	return "release_category"
}

// ReleaseGenre represents the release_genre junction table
type ReleaseGenre struct {
	ReleaseID string `gorm:"column:release_id;type:uuid;primaryKey"`
	GenreID   string `gorm:"column:genre_id;type:uuid;primaryKey"`
}

// TableName returns the table name for GORM
func (ReleaseGenre) TableName() string {
	return "release_genre"
}

// ReleaseStyle represents the release_style junction table
type ReleaseStyle struct {
	ReleaseID string `gorm:"column:release_id;type:uuid;primaryKey"`
	StyleID   string `gorm:"column:style_id;type:uuid;primaryKey"`
}

// TableName returns the table name for GORM
func (ReleaseStyle) TableName() string {
	return "release_style"
}

// ReleaseFormat represents the release_format junction table
type ReleaseFormat struct {
	ReleaseID         string  `gorm:"column:release_id;type:uuid;primaryKey"`
	FormatID          string  `gorm:"column:format_id;type:uuid;primaryKey"`
	FormatDescription *string `gorm:"column:format_description;type:varchar(255)"`
}

// TableName returns the table name for GORM
func (ReleaseFormat) TableName() string {
	return "release_format"
}

// Track model is defined in track.go
