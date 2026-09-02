package model

import (
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Label represents the label table (record label)
// Editor permissions are managed by SpiceDB.
type Label struct {
	ID                   string                      `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID    *string                     `gorm:"column:content_document_id;type:uuid"`
	SourceLocale         string                      `gorm:"column:source_locale;type:text;not null;default:en"`
	ContentDocument      *contentv1.RichTextDocument `gorm:"-"`
	ContentRevision      string                      `gorm:"-"`
	ContentCanonicalHash string                      `gorm:"-"`
	Name                 string                      `gorm:"-"`
	Slug                 *string                     `gorm:"column:slug;type:varchar(255);uniqueIndex"`
	Description          *string                     `gorm:"-"`
	DescriptionHTML      *string                     `gorm:"-"`
	DescriptionJSON      []byte                      `gorm:"-"`
	CountryCode          *string                     `gorm:"column:country_code;type:varchar(2)"`
	Website              *string                     `gorm:"column:website;type:varchar(500)"`
	LogoLightFileID      *string                     `gorm:"column:logo_light_file_id;type:uuid"`
	LogoDarkFileID       *string                     `gorm:"column:logo_dark_file_id;type:uuid"`
	SocialLinks          map[string]string           `gorm:"column:social_links;type:jsonb;serializer:json"`
	ParentLabelID        *string                     `gorm:"column:parent_label_id;type:uuid"`
	OgAssetID            *string                     `gorm:"column:og_asset_id;type:uuid"`
	Status               string                      `gorm:"column:status;type:varchar(50);not null;default:LABEL_STATUS_DRAFT"`
	PublishedAt          *time.Time                  `gorm:"column:published_at"`
	CreatedAt            time.Time                   `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            time.Time                   `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (Label) TableName() string {
	return "label"
}
