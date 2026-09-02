package model

import (
	"time"
)

// AudienceSegment represents the audience_segment table
type AudienceSegment struct {
	ID                           string                `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name                         string                `gorm:"column:name;type:varchar(100);not null"`
	Description                  *string               `gorm:"column:description;type:text"`
	SegmentType                  string                `gorm:"column:segment_type;type:varchar(30);not null"`
	CreatedAfter                 *time.Time            `gorm:"column:created_after"`
	CreatedBefore                *time.Time            `gorm:"column:created_before"`
	Config                       AudienceSegmentConfig `gorm:"-"`
	CampaignCount                int32                 `gorm:"-"`
	DeliveryRunCount             int32                 `gorm:"-"`
	DownloadPolicyReferenceCount int32                 `gorm:"-"`
	ArchivedAt                   *time.Time            `gorm:"column:archived_at;type:timestamptz"`
	CreatedAt                    time.Time             `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt                    *time.Time            `gorm:"column:updated_at"`
}

// TableName returns the table name for GORM
func (AudienceSegment) TableName() string {
	return "audience_segment"
}

// AudienceSegmentConfig is the in-memory projection of normalized segment fields.
type AudienceSegmentConfig struct {
	MemberTagIDs     []string `json:"member_tag_ids,omitempty"`
	AccountRoles     []string `json:"account_roles,omitempty"`
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string `json:"exclude_member_ids,omitempty"`
}

type AudienceSegmentUserTag struct {
	AudienceSegmentID string `gorm:"column:audience_segment_id;type:uuid;primaryKey"`
	UserTagID         string `gorm:"column:user_tag_id;type:uuid;primaryKey"`
}

func (AudienceSegmentUserTag) TableName() string {
	return "audience_segment_user_tag"
}

type AudienceSegmentUserRole struct {
	AudienceSegmentID string `gorm:"column:audience_segment_id;type:uuid;primaryKey"`
	Role              string `gorm:"column:role;type:varchar(30);primaryKey"`
}

func (AudienceSegmentUserRole) TableName() string {
	return "audience_segment_user_role"
}

type AudienceSegmentExcludedMember struct {
	AudienceSegmentID string `gorm:"column:audience_segment_id;type:uuid;primaryKey"`
	MemberID          string `gorm:"column:member_id;type:uuid;primaryKey"`
}

func (AudienceSegmentExcludedMember) TableName() string {
	return "audience_segment_excluded_member"
}
