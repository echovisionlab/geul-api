package mediaasset

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type DownloadAuthorizer interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}
type DownloadMemberFacts struct {
	MemberID   string
	IdentityID string
	Onboarded  bool
	CreatedAt  time.Time
}
type DownloadTagFact struct {
	TagID          string
	MappedMemberID *string
}
type DownloadAssociation struct {
	PolicyKey         string
	AudienceSegmentID string
}
type DownloadSegmentConfig struct {
	MemberTagIDs     []string `json:"member_tag_ids,omitempty"`
	AccountRoles     []string `json:"account_roles,omitempty"`
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string `json:"exclude_member_ids,omitempty"`
}
type DownloadSegmentType string

const (
	DownloadAllMembers      DownloadSegmentType = "SEGMENT_TYPE_ALL_MEMBERS"
	DownloadMemberTags      DownloadSegmentType = "SEGMENT_TYPE_MEMBER_TAGS"
	DownloadMembersByFilter DownloadSegmentType = "SEGMENT_TYPE_MEMBERS_BY_FILTER"
)

type DownloadSegment struct {
	ID          string
	SegmentType DownloadSegmentType
	ArchivedAt  *time.Time
	UpdatedAt   *time.Time
	Config      DownloadSegmentConfig
}
type DownloadStore interface {
	ContentSources(context.Context, []string) ([]FileDownloadSource, error)
	TrackSources(context.Context, []string) ([]FileDownloadSource, error)
	Associations(context.Context, map[string]FileDownloadSource, bool) ([]DownloadAssociation, error)
	Segments(context.Context, []string, bool) ([]DownloadSegment, error)
	MemberFacts(context.Context, string, string) (DownloadMemberFacts, bool, error)
	TagFacts(context.Context, string, []string) ([]DownloadTagFact, error)
	ViewerTagIDs(context.Context, string) ([]string, error)
	LockSegments(context.Context, []string) error
	LockViewer(context.Context, *auth.UserInfo, bool) (bool, error)
}
