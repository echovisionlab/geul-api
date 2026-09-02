package model

import (
	"time"
)

// Privacy represents a privacy policy version
// Maps to: sql/001_schema.sql - privacy_history table
type Privacy struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Version           int        `gorm:"column:version;not null"`
	Title             string     `gorm:"column:title;type:varchar(255);not null"`
	Content           string     `gorm:"column:content;type:text;not null"`
	ContentText       *string    `gorm:"column:content_text;type:text"`
	ContentHash       *string    `gorm:"column:content_hash;type:varchar(64)"`
	ViewHash          *string    `gorm:"column:view_hash;type:varchar(64)"`
	ContentDocumentID *string    `gorm:"column:content_document_id;type:uuid;uniqueIndex"`
	Status            string     `gorm:"column:status;type:varchar(50);not null;default:PRIVACY_STATUS_DRAFT"`
	EffectiveFrom     *time.Time `gorm:"column:effective_from"`
	EffectiveUntil    *time.Time `gorm:"column:effective_until"`
	CreatedAt         time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (Privacy) TableName() string {
	return "privacy_history"
}
