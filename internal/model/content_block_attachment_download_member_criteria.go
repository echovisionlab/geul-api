package model

import "time"

type ContentBlockAttachmentDownloadMemberCriteria struct {
	BlockID          string    `gorm:"column:block_id;primaryKey"`
	ReferencePath    string    `gorm:"column:reference_path;primaryKey"`
	MemberCriteriaID string    `gorm:"column:member_criteria_id;primaryKey"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

func (ContentBlockAttachmentDownloadMemberCriteria) TableName() string {
	return "content_block_attachment_download_member_criteria"
}
