package model

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/lib/pq"
)

// Work represents the work table
type Work struct {
	ID                   string                             `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID    *string                            `gorm:"column:content_document_id;type:uuid;uniqueIndex"`
	SourceLocale         string                             `gorm:"column:source_locale;type:text;not null;default:en"`
	ContentDocument      *contentv1.RichTextDocument        `gorm:"-"`
	ContentRevision      string                             `gorm:"-"`
	ContentCanonicalHash string                             `gorm:"-"`
	BlockMedia           []*contentv1.ContentBlockMediaItem `gorm:"-"`
	Title                string                             `gorm:"-"`
	Slug                 *string                            `gorm:"column:slug;type:varchar(255);uniqueIndex"`
	Type                 string                             `gorm:"column:type;type:varchar(30);not null;default:WORK_TYPE_MUSIC_PROJECT"`
	Year                 int32                              `gorm:"column:year;type:int;not null"`
	Month                int32                              `gorm:"column:month;type:int;not null"`
	UntilYear            *int32                             `gorm:"column:until_year;type:int"`
	UntilMonth           *int32                             `gorm:"column:until_month;type:int"`
	IsPresent            bool                               `gorm:"column:is_present;type:boolean;not null;default:false"`
	Summary              *string                            `gorm:"-"`
	MapPlaceID           *string                            `gorm:"column:map_place_id;type:uuid"`
	FeaturedImageFileID  *string                            `gorm:"column:featured_image_file_id;type:uuid"`
	Metadata             structured.Fields                  `gorm:"column:metadata;type:jsonb;serializer:json"`
	Featured             bool                               `gorm:"column:featured;type:boolean;not null;default:false"`
	Status               string                             `gorm:"column:status;type:varchar(50);not null;default:WORK_STATUS_DRAFT"`
	OgAssetID            *string                            `gorm:"column:og_asset_id;type:uuid"`
	CreatedAt            time.Time                          `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            time.Time                          `gorm:"column:updated_at;not null;default:now()"`
	PublishedAt          *time.Time                         `gorm:"column:published_at"`

	MapPlace *MapPlace `gorm:"foreignKey:MapPlaceID"`
}

// TableName returns the table name for GORM
func (Work) TableName() string {
	return "work"
}

// WorkVersion represents a version snapshot of a work
type WorkVersion struct {
	ID                   string         `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkID               string         `gorm:"column:work_id"`
	Version              int32          `gorm:"column:version"`
	Title                *string        `gorm:"column:title"`
	Summary              *string        `gorm:"column:summary"`
	ContentSnapshot      []byte         `gorm:"column:content_snapshot;type:jsonb"`
	ContributorMemberIDs pq.StringArray `gorm:"column:contributor_member_ids;type:uuid[];not null;default:'{}'"`
	CreatedAt            time.Time      `gorm:"column:created_at"`
}

func (WorkVersion) TableName() string {
	return "work_version"
}

// WorkCreditGroup represents the work_credit_group table
type WorkCreditGroup struct {
	ID        string `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	WorkID    string `gorm:"column:work_id;type:uuid;not null"`
	Name      string `gorm:"column:name;type:varchar(255);not null"`
	SortOrder int    `gorm:"column:sort_order;type:int;not null;default:0"`
}

// TableName returns the table name for GORM
func (WorkCreditGroup) TableName() string {
	return "work_credit_group"
}

// WorkCredit represents the work_credit table
type WorkCredit struct {
	ID         string  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	WorkID     string  `gorm:"column:work_id;type:uuid;not null"`
	GroupID    *string `gorm:"column:group_id;type:uuid"`
	ArtistID   *string `gorm:"column:artist_id;type:uuid"`
	MemberID   *string `gorm:"column:member_id;type:uuid"`
	Name       *string `gorm:"column:name;type:varchar(255)"`
	CreditRole *string `gorm:"column:credit_role;type:varchar(100)"`
	SortOrder  int     `gorm:"column:sort_order;type:int;not null;default:0"`
}

// TableName returns the table name for GORM
func (WorkCredit) TableName() string {
	return "work_credit"
}
