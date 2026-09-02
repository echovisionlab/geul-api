package model

import "time"

// FileIngestBinding records the immutable owner and purpose of an uploaded source file.
type FileIngestBinding struct {
	FileID     string    `gorm:"column:file_id;type:uuid;primaryKey"`
	UploadType string    `gorm:"column:upload_type;type:text;not null"`
	EntityType *string   `gorm:"column:entity_type;type:text"`
	EntityID   string    `gorm:"column:entity_id;type:text"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;default:now()"`
}

func (FileIngestBinding) TableName() string { return "file_ingest_binding" }
