package model

import (
	"encoding/json"
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/lib/pq"
)

// PostStatus represents the status of a post
type PostStatus string

// Post represents a blog post (Domain Object)
// Maps to: schema migrations - post table
type Post struct {
	ID                    string                             `gorm:"column:id;primaryKey"`
	ContentDocumentID     *string                            `gorm:"column:content_document_id;type:uuid"`
	SourceLocale          string                             `gorm:"column:source_locale;type:text;not null;default:en"`
	ContentDocument       *contentv1.RichTextDocument        `gorm:"-"`
	ContentRevision       string                             `gorm:"-"`
	ContentCanonicalHash  string                             `gorm:"-"`
	BlockMedia            []*contentv1.ContentBlockMediaItem `gorm:"-"`
	Title                 string                             `gorm:"-"`
	Slug                  *string                            `gorm:"column:slug"`
	Summary               *string                            `gorm:"-"`
	DocumentLayout        DocumentLayout                     `gorm:"column:document_layout;type:jsonb;not null"`
	Status                PostStatus                         `gorm:"column:status"`
	CommentsEnabled       bool                               `gorm:"column:comments_enabled"`
	SeriesID              *string                            `gorm:"column:series_id"`
	SeriesOrder           *int32                             `gorm:"column:series_order"`
	MapPlaceID            *string                            `gorm:"column:map_place_id"`
	FeaturedImageFileID   *string                            `gorm:"column:featured_image_file_id;type:uuid"`
	OgAssetID             *string                            `gorm:"column:og_asset_id;type:uuid"`
	SourceLocaleOgAssetID *string                            `gorm:"-"`
	PublishedAt           *time.Time                         `gorm:"column:published_at"`
	ScheduledAt           *time.Time                         `gorm:"column:scheduled_at"`
	ScheduledTimeZone     *string                            `gorm:"column:scheduled_time_zone"`
	CreatedAt             time.Time                          `gorm:"column:created_at"`
	UpdatedAt             time.Time                          `gorm:"column:updated_at"`

	// Relations (loaded with Preload)
	Categories    []Category `gorm:"many2many:post_category;"`
	Tags          []Tag      `gorm:"many2many:post_tag;"`
	Series        *Series    `gorm:"foreignKey:SeriesID"`
	MapPlace      *MapPlace  `gorm:"foreignKey:MapPlaceID"`
	FeaturedImage *File      `gorm:"foreignKey:FeaturedImageFileID"`
}

func (Post) TableName() string {
	return "post"
}

// Category represents a post category
// Maps to: schema migrations - category table
// Note: No sort_order column - ordered by name
type Category struct {
	ID          string     `gorm:"column:id;primaryKey"`
	Name        string     `gorm:"column:name"`
	Slug        string     `gorm:"column:slug"` // NOT NULL in schema
	Description *string    `gorm:"column:description"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   *time.Time `gorm:"column:updated_at"`
}

func (Category) TableName() string {
	return "category"
}

// Tag represents a post tag
// Maps to: schema migrations - tag table
// Note: No updated_at column
type Tag struct {
	ID        string    `gorm:"column:id;primaryKey"`
	Name      string    `gorm:"column:name"`
	Slug      string    `gorm:"column:slug"` // NOT NULL in schema
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (Tag) TableName() string {
	return "tag"
}

// Series represents a post series
// Maps to: schema migrations - series table
// Note: Series membership is managed by SpiceDB, not series_member table.
type Series struct {
	ID                  string     `gorm:"column:id;primaryKey"`
	ContentDocumentID   string     `gorm:"column:content_document_id;type:uuid"`
	Title               string     `gorm:"-"`
	Slug                string     `gorm:"column:slug"` // NOT NULL in schema
	Description         *string    `gorm:"-"`
	Status              string     `gorm:"column:status"` // draft, published
	SourceLocale        string     `gorm:"column:source_locale"`
	FeaturedImageFileID *string    `gorm:"column:featured_image_file_id;type:uuid"`
	OgAssetID           *string    `gorm:"-"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           *time.Time `gorm:"column:updated_at"`
}

type SeriesTranslation struct {
	EntityID    string    `gorm:"column:entity_id;type:uuid;primaryKey"`
	Locale      string    `gorm:"column:locale;type:text;primaryKey"`
	Title       *string   `gorm:"column:title;type:text"`
	Summary     *string   `gorm:"column:summary;type:text"`
	ContentJSON []byte    `gorm:"column:content_json;type:jsonb"`
	ContentHTML *string   `gorm:"column:content_html;type:text"`
	ContentText *string   `gorm:"column:content_text;type:text"`
	CreatedAt   time.Time `gorm:"column:created_at;not null"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null"`
	OgAssetID   *string   `gorm:"column:og_asset_id;type:uuid"`
}

func (Series) TableName() string {
	return "series"
}

// PostVersion represents a version snapshot of a post
type PostVersion struct {
	ID                   string          `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PostID               string          `gorm:"column:post_id"`
	Version              int32           `gorm:"column:version"`
	ContentSnapshot      json.RawMessage `gorm:"column:content_snapshot;type:jsonb"`
	ContributorMemberIDs pq.StringArray  `gorm:"column:contributor_member_ids;type:uuid[];not null;default:'{}'"`
	CreatedAt            time.Time       `gorm:"column:created_at"`
}

func (PostVersion) TableName() string {
	return "post_version"
}

// ShareLink represents a share link for various entities
// Maps to: schema migrations - share_link table
type ShareLink struct {
	ID           string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	Token        string     `gorm:"column:token;uniqueIndex"` // Cryptographically secure random token
	EntityType   string     `gorm:"column:entity_type"`       // post, work, etc.
	EntityID     string     `gorm:"column:entity_id"`
	Label        *string    `gorm:"column:label"`
	PasswordHash *string    `gorm:"column:password_hash"`
	ExpiresAt    *time.Time `gorm:"column:expires_at;not null"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
}

func (ShareLink) TableName() string {
	return "share_link"
}
