package criteria

import (
	"errors"
	"strings"
	"time"
)

// Kind is the stored kind of a criteria. The values are the strings
// the member_criteria table holds, which equal the API enum names.
type Kind string

const (
	KindAllMembers Kind = "MEMBER_CRITERIA_KIND_ALL_MEMBERS"
	KindMemberTags Kind = "MEMBER_CRITERIA_KIND_MEMBER_TAGS"
	KindFilter     Kind = "MEMBER_CRITERIA_KIND_FILTER"

	// kindUnspecified is the API name of an absent criteria type. It is
	// never stored; a create or update that carries it is rejected.
	kindUnspecified Kind = "MEMBER_CRITERIA_KIND_UNSPECIFIED"
)

// trimmed is the type with surrounding blanks removed, as validation and
// the authenticated-access checks read it.
func (t Kind) trimmed() Kind {
	return Kind(strings.TrimSpace(string(t)))
}

// Criteria is a stored Member criteria and its filter.
type Criteria struct {
	ID          string
	Name        string
	Description *string
	Type        Kind
	Config      Config
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   *time.Time
}

// Config is a criteria's filter: canonical, sorted and deduplicated
// identifiers and the Member creation window. AccountRoles are role IDs
// (policyv1.Role).
type Config struct {
	MemberTagIDs     []string
	AccountRoles     []string
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string
}

// References counts what targets a criteria.
type References struct {
	Campaigns        int32
	DeliveryRuns     int32
	DownloadPolicies int32
	// SignInApps admit Members through the criteria; an archived criteria
	// admits nobody until it is restored.
	SignInApps int32
}

// CriteriaView is a criteria as the use cases return it: with what targets
// it and, from GetCriteria when the counter answers, its estimated recipients.
type CriteriaView struct {
	Criteria       Criteria
	References     References
	EstimatedCount *int32
}

// CriteriaChange is the stored change of one update. A nil pointer and an
// unset time leave the column as it is.
type CriteriaChange struct {
	Name             *string
	Description      *string
	Type             *Kind
	SetCreatedAfter  bool
	CreatedAfter     *time.Time
	SetCreatedBefore bool
	CreatedBefore    *time.Time
	UpdatedAt        time.Time
}

func (c CriteriaChange) changed() bool {
	return c.Name != nil || c.Description != nil || c.Type != nil || c.SetCreatedAfter || c.SetCreatedBefore
}

// CampaignUsage is what LockCampaignUsage locked: the criteria's campaigns and
// its scheduled and sending delivery runs, by ID.
type CampaignUsage struct {
	CampaignIDs    []string
	DeliveryRunIDs []string
}

// DownloadPolicy is a File attachment's or a Track's restricted download
// policy that grants through a criteria.
type DownloadPolicy struct {
	// Owner is the owning document's domain: post, page, work, program_event,
	// or release for a Track.
	Owner   string
	OwnerID string
	// ItemID is the File Block or the Track.
	ItemID   string
	FileID   string
	Criteria string
	// CriteriaIDs are every criteria the policy grants through, sorted.
	CriteriaIDs []string
}

// The outcomes of a Store the use cases branch on.
var (
	ErrCriteriaNotFound = errors.New("Member criteria not found")
	// ErrArchiveStateChanged rejects an archive of an archived criteria or a
	// restore of an active one.
	ErrArchiveStateChanged = errors.New("Member criteria archive state changed")
	// ErrUnknownReference rejects a filter naming a Member tag or a Member
	// that does not exist.
	ErrUnknownReference = errors.New("Member criteria references a missing Member tag or Member")
)
