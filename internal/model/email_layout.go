package model

import (
	"time"
)

// EmailLayout represents an email layout template (header/footer wrapper)
type EmailLayout struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID string    `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string    `gorm:"column:source_locale;type:text;not null;default:en"`
	Name              string    `gorm:"column:name;type:varchar(255);not null"`
	Key               string    `gorm:"column:key;type:varchar(100);uniqueIndex;not null"`
	HTMLContent       string    `gorm:"-"`
	CreatedAt         time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:now()"`
	CampaignCount     int32     `gorm:"-"`
	TemplateCount     int32     `gorm:"-"`
	DeliveryRunCount  int32     `gorm:"-"`
}

func (EmailLayout) TableName() string {
	return "email_layout"
}
