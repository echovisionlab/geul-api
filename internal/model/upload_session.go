package model

import "time"

type UploadSessionStatus string

const (
	UploadSessionStatusInitiated  UploadSessionStatus = "initiated"
	UploadSessionStatusUploading  UploadSessionStatus = "uploading"
	UploadSessionStatusFinalizing UploadSessionStatus = "finalizing"
	UploadSessionStatusFailed     UploadSessionStatus = "failed"
	UploadSessionStatusAborted    UploadSessionStatus = "aborted"
)

// UploadSession tracks multipart uploads for MIME verification.
type UploadSession struct {
	UploadID         string              `gorm:"column:upload_id;type:text;primaryKey"`
	FileID           string              `gorm:"column:file_id;type:uuid;not null"`
	UploadType       string              `gorm:"column:upload_type;type:text;not null"`
	EntityID         string              `gorm:"column:entity_id;type:text"`
	EntityType       *string             `gorm:"column:entity_type;type:text"`
	FileName         string              `gorm:"column:file_name;type:text;not null"`
	FileSize         int64               `gorm:"column:file_size;type:bigint;not null"`
	FileLastModified *int64              `gorm:"column:file_last_modified;type:bigint"`
	SlotID           *string             `gorm:"column:slot_id;type:text"`
	AttemptID        *string             `gorm:"column:attempt_id;type:text"`
	ExpectedFileID   *string             `gorm:"column:expected_current_file_id;type:uuid"`
	IngestSequence   int64               `gorm:"column:ingest_sequence;type:bigint;not null;default:0"`
	RequestedMime    string              `gorm:"column:requested_mime;type:text;not null"`
	DetectedMime     *string             `gorm:"column:detected_mime;type:text"`
	TotalParts       int32               `gorm:"column:total_parts;type:integer;not null"`
	ChunkSize        int32               `gorm:"column:chunk_size;type:integer;not null"`
	Status           UploadSessionStatus `gorm:"column:status;type:text;not null"`
	VerifiedAt       *time.Time          `gorm:"column:verified_at"`
	LastActivityAt   time.Time           `gorm:"column:last_activity_at;not null;default:now()"`
	CreatedAt        time.Time           `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt        time.Time           `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for GORM.
func (UploadSession) TableName() string {
	return "upload_session"
}
