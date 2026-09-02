package model

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Artist represents the artist table
type Artist struct {
	ID                   string                      `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID    *string                     `gorm:"column:content_document_id;type:uuid"`
	SourceLocale         string                      `gorm:"column:source_locale;type:text;not null;default:en"`
	ContentDocument      *contentv1.RichTextDocument `gorm:"-"`
	ContentRevision      string                      `gorm:"-"`
	ContentCanonicalHash string                      `gorm:"-"`
	Name                 string                      `gorm:"-"`
	Slug                 *string                     `gorm:"column:slug;type:varchar(255);uniqueIndex"`
	RealName             *string                     `gorm:"column:real_name;type:varchar(255)"`
	ParentArtistID       *string                     `gorm:"column:parent_artist_id;type:uuid"`
	Bio                  *string                     `gorm:"-"`
	BioHTML              *string                     `gorm:"-"`
	BioJSON              []byte                      `gorm:"-"`
	CountryCode          *string                     `gorm:"column:country_code;type:varchar(2)"`
	Website              *string                     `gorm:"column:website;type:varchar(500)"`
	SocialLinks          map[string]string           `gorm:"column:social_links;type:jsonb;serializer:json"`
	Metadata             structured.Fields           `gorm:"column:metadata;type:jsonb;serializer:json"`
	OgAssetID            *string                     `gorm:"column:og_asset_id;type:uuid"`
	Status               string                      `gorm:"column:status;type:varchar(50);not null;default:ARTIST_STATUS_DRAFT"`
	PublishedAt          *time.Time                  `gorm:"column:published_at"`
	CreatedAt            time.Time                   `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            *time.Time                  `gorm:"column:updated_at"`
}

// TableName returns the table name for GORM
func (Artist) TableName() string {
	return "artist"
}

// ArtistFile represents the artist_file junction table
type ArtistFile struct {
	ArtistID  string    `gorm:"column:artist_id;type:uuid;primaryKey"`
	FileID    string    `gorm:"column:file_id;type:uuid;primaryKey"`
	SortOrder int       `gorm:"column:sort_order;type:int;not null;default:0"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (ArtistFile) TableName() string {
	return "artist_file"
}

// File represents the file table
type File struct {
	ID                 string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	FileName           string     `gorm:"column:file_name;type:text;not null"`
	MimeType           string     `gorm:"column:mime_type;type:text;not null"`
	FileSize           int64      `gorm:"column:file_size;type:bigint;not null"`
	Extension          string     `gorm:"column:extension;type:text;not null"`
	SHA256             []byte     `gorm:"column:sha256;type:bytea"`
	DurationSeconds    *int       `gorm:"column:duration_seconds"`
	IngestSlotID       *string    `gorm:"column:ingest_slot_id;type:text"`
	IngestAttemptID    *string    `gorm:"column:ingest_attempt_id;type:text"`
	FolderID           *string    `gorm:"column:folder_id;type:uuid;->"`
	UploadedByMemberID *string    `gorm:"column:uploaded_by_member_id;type:uuid;->"`
	DeleteRequestedAt  *time.Time `gorm:"column:delete_requested_at"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;->"`
}

// TableName returns the table name for GORM
func (File) TableName() string {
	return "file"
}

// FileFolder is a virtual File Manager location. It never changes the object key.
type FileFolder struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ParentID          *string   `gorm:"column:parent_id;type:uuid"`
	Name              string    `gorm:"column:name;type:text;not null"`
	CreatedByMemberID *string   `gorm:"column:created_by_member_id;type:uuid"`
	CreatedAt         time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (FileFolder) TableName() string { return "file_folder" }

// ArtistLabel represents the artist_label junction table
type ArtistLabel struct {
	ArtistID string `gorm:"column:artist_id;type:uuid;primaryKey"`
	LabelID  string `gorm:"column:label_id;type:uuid;primaryKey"`
}

// TableName returns the table name for GORM
func (ArtistLabel) TableName() string {
	return "artist_label"
}

// Note: Artist editor permissions are managed by SpiceDB.
