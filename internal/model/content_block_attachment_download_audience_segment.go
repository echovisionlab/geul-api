package model

import "time"

type ContentBlockAttachmentDownloadAudienceSegment struct {
	BlockID           string    `gorm:"column:block_id;primaryKey"`
	ReferencePath     string    `gorm:"column:reference_path;primaryKey"`
	AudienceSegmentID string    `gorm:"column:audience_segment_id;primaryKey"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (ContentBlockAttachmentDownloadAudienceSegment) TableName() string {
	return "content_block_attachment_download_audience_segment"
}
