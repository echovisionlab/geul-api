package model

import (
	"time"
)

// MemberCriteriaKind is the stored kind of a Member criteria. The values
// are the strings the member_criteria table holds; internal/criteria maps
// them to and from the API enum.
type MemberCriteriaKind string

const (
	MemberCriteriaKindAllMembers MemberCriteriaKind = "MEMBER_CRITERIA_KIND_ALL_MEMBERS"
	MemberCriteriaKindMemberTags MemberCriteriaKind = "MEMBER_CRITERIA_KIND_MEMBER_TAGS"
	MemberCriteriaKindFilter     MemberCriteriaKind = "MEMBER_CRITERIA_KIND_FILTER"
)

// MemberCriteria represents the member_criteria table
type MemberCriteria struct {
	ID                           string               `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name                         string               `gorm:"column:name;type:varchar(100);not null"`
	Description                  *string              `gorm:"column:description;type:text"`
	Kind                         MemberCriteriaKind   `gorm:"column:kind;type:varchar(40);not null"`
	CreatedAfter                 *time.Time           `gorm:"column:created_after"`
	CreatedBefore                *time.Time           `gorm:"column:created_before"`
	Config                       MemberCriteriaConfig `gorm:"-"`
	CampaignCount                int32                `gorm:"-"`
	DeliveryRunCount             int32                `gorm:"-"`
	DownloadPolicyReferenceCount int32                `gorm:"-"`
	ArchivedAt                   *time.Time           `gorm:"column:archived_at;type:timestamptz"`
	CreatedAt                    time.Time            `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt                    *time.Time           `gorm:"column:updated_at"`
}

// TableName returns the table name for GORM
func (MemberCriteria) TableName() string {
	return "member_criteria"
}

// MemberCriteriaConfig is the in-memory projection of normalized criteria fields.
type MemberCriteriaConfig struct {
	MemberTagIDs     []string `json:"member_tag_ids,omitempty"`
	AccountRoles     []string `json:"account_roles,omitempty"`
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string `json:"exclude_member_ids,omitempty"`
}

type MemberCriteriaTag struct {
	MemberCriteriaID string `gorm:"column:member_criteria_id;type:uuid;primaryKey"`
	UserTagID        string `gorm:"column:user_tag_id;type:uuid;primaryKey"`
}

func (MemberCriteriaTag) TableName() string {
	return "member_criteria_tag"
}

type MemberCriteriaRole struct {
	MemberCriteriaID string `gorm:"column:member_criteria_id;type:uuid;primaryKey"`
	Role             string `gorm:"column:role;type:varchar(30);primaryKey"`
}

func (MemberCriteriaRole) TableName() string {
	return "member_criteria_role"
}

type MemberCriteriaExcludedMember struct {
	MemberCriteriaID string `gorm:"column:member_criteria_id;type:uuid;primaryKey"`
	MemberID         string `gorm:"column:member_id;type:uuid;primaryKey"`
}

func (MemberCriteriaExcludedMember) TableName() string {
	return "member_criteria_excluded_member"
}
