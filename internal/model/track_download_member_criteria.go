package model

import "time"

type TrackDownloadMemberCriteria struct {
	TrackID          string    `gorm:"column:track_id;primaryKey"`
	MemberCriteriaID string    `gorm:"column:member_criteria_id;primaryKey"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

func (TrackDownloadMemberCriteria) TableName() string {
	return "track_download_member_criteria"
}
