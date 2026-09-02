package model

import (
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// FileDerivative represents a derivative file (thumbnail, HLS, preview, etc.)
// Maps to: sql/003_file_derivative.sql - file_derivative table
type FileDerivative struct {
	ID                string                      `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	FileID            string                      `gorm:"column:file_id;type:uuid;not null"`
	Type              managev1.FileDerivativeType `gorm:"column:type;type:text;not null"`
	AssetID           *string                     `gorm:"column:asset_id;type:uuid"`
	MediaGenerationID *string                     `gorm:"column:media_generation_id;type:uuid"`
	CreatedAt         time.Time                   `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (FileDerivative) TableName() string {
	return "file_derivative"
}
