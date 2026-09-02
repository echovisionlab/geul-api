package model

import (
	"time"
)

const (
	CampaignTargetModeAll     = "all"
	CampaignTargetModeSegment = "segment"
)

// Campaign represents an email campaign
type Campaign struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID *string    `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string     `gorm:"column:source_locale;type:text;not null;default:en"`
	Name              string     `gorm:"column:name;type:varchar(255);not null;default:''"`
	Subject           string     `gorm:"column:subject;type:varchar(500);not null;default:''"`
	ContentHTML       *string    `gorm:"-"`
	Status            string     `gorm:"column:status;type:varchar(50);not null;default:CAMPAIGN_STATUS_DRAFT"`
	TargetMode        string     `gorm:"column:target_mode;type:varchar(16);not null"`
	SegmentID         *string    `gorm:"column:segment_id;type:uuid"`
	LayoutID          *string    `gorm:"column:layout_id;type:uuid"`
	ScheduledAt       *time.Time `gorm:"column:scheduled_at;type:timestamptz"`
	SentAt            *time.Time `gorm:"column:sent_at;type:timestamptz"`
	SentCount         int        `gorm:"column:sent_count;not null;default:0"`
	RecipientScope    string     `gorm:"column:recipient_scope;type:varchar(32);not null;default:'SUBSCRIBED_USERS'"`
	CreatedAt         time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (Campaign) TableName() string {
	return "campaign"
}
