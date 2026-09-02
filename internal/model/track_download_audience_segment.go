package model

import "time"

type TrackDownloadAudienceSegment struct {
	TrackID           string    `gorm:"column:track_id;primaryKey"`
	AudienceSegmentID string    `gorm:"column:audience_segment_id;primaryKey"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (TrackDownloadAudienceSegment) TableName() string {
	return "track_download_audience_segment"
}
