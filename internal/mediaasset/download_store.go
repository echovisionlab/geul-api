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
	PolicyKey        string
	MemberCriteriaID string
}
type DownloadCriteriaConfig struct {
	MemberTagIDs     []string `json:"member_tag_ids,omitempty"`
	AccountRoles     []string `json:"account_roles,omitempty"`
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string `json:"exclude_member_ids,omitempty"`
}
type DownloadKind string

const (
	DownloadAllMembers      DownloadKind = "MEMBER_CRITERIA_KIND_ALL_MEMBERS"
	DownloadMemberTags      DownloadKind = "MEMBER_CRITERIA_KIND_MEMBER_TAGS"
	DownloadMembersByFilter DownloadKind = "MEMBER_CRITERIA_KIND_FILTER"
)

type DownloadCriteria struct {
	ID         string
	Kind       DownloadKind
	ArchivedAt *time.Time
	UpdatedAt  *time.Time
	Config     DownloadCriteriaConfig
}
type DownloadStore interface {
	ContentSources(context.Context, []string) ([]FileDownloadSource, error)
	TrackSources(context.Context, []string) ([]FileDownloadSource, error)
	Associations(context.Context, map[string]FileDownloadSource, bool) ([]DownloadAssociation, error)
	CriteriaSet(context.Context, []string, bool) ([]DownloadCriteria, error)
	MemberFacts(context.Context, string, string) (DownloadMemberFacts, bool, error)
	TagFacts(context.Context, string, []string) ([]DownloadTagFact, error)
	ViewerTagIDs(context.Context, string) ([]string, error)
	LockCriteriaSet(context.Context, []string) error
	LockViewer(context.Context, *auth.UserInfo, bool) (bool, error)
}
