package model

import "time"

// UploadPart tracks successfully uploaded multipart chunks for resumable uploads.
type UploadPart struct {
	UploadID   string    `gorm:"column:upload_id;type:text;primaryKey"`
	PartNumber int32     `gorm:"column:part_number;type:integer;primaryKey"`
	ETag       string    `gorm:"column:etag;type:text;not null"`
	Size       int64     `gorm:"column:size;type:bigint;not null"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt  time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (UploadPart) TableName() string {
	return "upload_part"
}
