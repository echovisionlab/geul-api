package model

import (
	"time"
)

// TrackMetadata represents track metadata stored as JSONB
type TrackMetadata = JSONFields

// Track represents a track in a release
// Maps to: sql/001_schema.sql - track table
type Track struct {
	ID                  string        `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ReleaseID           string        `gorm:"column:release_id;type:uuid;not null"`
	TrackNumber         int           `gorm:"column:track_number;not null"`
	Title               string        `gorm:"column:title;type:varchar(255);not null"`
	DurationSeconds     *int          `gorm:"column:duration_seconds"`
	ProcessingStatus    *string       `gorm:"column:processing_status;type:varchar(50)"`
	Lyrics              *string       `gorm:"column:lyrics;type:text"`
	Metadata            TrackMetadata `gorm:"column:metadata;type:jsonb;default:'{}'::jsonb"`
	CreatedAt           time.Time     `gorm:"column:created_at;not null;default:now()"`
	AudioOriginalFileID *string       `gorm:"column:audio_original_file_id;type:uuid"`
	DownloadAudience    string        `gorm:"column:download_audience;type:text;not null;default:disabled"`
}

// TableName returns the table name for GORM
func (Track) TableName() string {
	return "track"
}

// TrackCredit represents a credit on a track
// Maps to: sql/001_schema.sql - track_credit table
type TrackCredit struct {
	ID           string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	TrackID      string    `gorm:"column:track_id;type:uuid;not null"`
	ArtistID     *string   `gorm:"column:artist_id;type:uuid"`
	MemberID     *string   `gorm:"column:member_id;type:uuid"`
	CreditedName *string   `gorm:"column:credited_name;type:varchar(255)"`
	CreditRole   *string   `gorm:"column:credit_role;type:varchar(100)"`
	SortOrder    int       `gorm:"column:sort_order;default:0"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (TrackCredit) TableName() string {
	return "track_credit"
}

// TrackCreditWithInfo represents a track credit with joined artist/Member info.
type TrackCreditWithInfo struct {
	ID                string  `gorm:"column:id"`
	CreditType        string  `gorm:"column:credit_type"`
	ArtistID          *string `gorm:"column:artist_id"`
	ArtistName        *string `gorm:"column:artist_name"`
	ArtistSlug        *string `gorm:"column:artist_slug"`
	MemberID          *string `gorm:"column:member_id"`
	MemberDisplayName *string `gorm:"column:member_display_name"`
	CreditedName      *string `gorm:"column:credited_name"`
	CreditRole        *string `gorm:"column:credit_role"`
	SortOrder         int     `gorm:"column:sort_order"`
}
