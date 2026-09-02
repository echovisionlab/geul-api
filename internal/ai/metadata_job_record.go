package ai

import "time"

type metadataJobRecord struct {
	ID                string            `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	RequesterMemberID string            `gorm:"column:requester_member_id;type:uuid;not null"`
	TargetType        string            `gorm:"column:target_type;type:varchar(100);not null"`
	TargetID          string            `gorm:"column:target_id;type:text;not null"`
	RequestedKeys     []string          `gorm:"column:requested_keys;type:jsonb;serializer:json"`
	Context           string            `gorm:"column:context;type:text;not null"`
	Prompt            string            `gorm:"column:prompt;type:text;not null"`
	Status            string            `gorm:"column:status;type:varchar(50);not null"`
	Suggestion        map[string]string `gorm:"column:suggestion;type:jsonb;serializer:json"`
	ResponseText      *string           `gorm:"column:response_text;type:text"`
	Error             *string           `gorm:"column:error;type:text"`
	Provider          *string           `gorm:"column:provider;type:varchar(100)"`
	Model             *string           `gorm:"column:model;type:varchar(255)"`
	DurationMS        *int64            `gorm:"column:duration_ms"`
	StartedAt         *time.Time        `gorm:"column:started_at"`
	CompletedAt       *time.Time        `gorm:"column:completed_at"`
	ResolvedAt        *time.Time        `gorm:"column:resolved_at"`
	CreatedAt         time.Time         `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time         `gorm:"column:updated_at;not null;default:now()"`
}

func (metadataJobRecord) TableName() string {
	return "metadata_ai_job"
}
