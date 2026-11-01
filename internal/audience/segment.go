package audience

import (
	"errors"
	"strings"
	"time"
)

// SegmentType is the stored kind of a segment. The values are the strings
// the audience_segment table holds, which equal the API enum names.
type SegmentType string

const (
	SegmentAllMembers      SegmentType = "SEGMENT_TYPE_ALL_MEMBERS"
	SegmentMemberTags      SegmentType = "SEGMENT_TYPE_MEMBER_TAGS"
	SegmentMembersByFilter SegmentType = "SEGMENT_TYPE_MEMBERS_BY_FILTER"

	// segmentTypeUnspecified is the API name of an absent segment type. It is
	// never stored; a create or update that carries it is rejected.
	segmentTypeUnspecified SegmentType = "SEGMENT_TYPE_UNSPECIFIED"
)

// trimmed is the type with surrounding blanks removed, as validation and
// the authenticated-access checks read it.
func (t SegmentType) trimmed() SegmentType {
	return SegmentType(strings.TrimSpace(string(t)))
}

// Segment is a stored audience segment and its filter.
type Segment struct {
	ID          string
	Name        string
	Description *string
	Type        SegmentType
	Config      Config
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   *time.Time
}

// Config is a segment's filter: canonical, sorted and deduplicated
// identifiers and the Member creation window. AccountRoles are role IDs
// (policyv1.Role).
type Config struct {
	MemberTagIDs     []string
	AccountRoles     []string
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string
}

// References counts what targets a segment.
type References struct {
	Campaigns        int32
	DeliveryRuns     int32
	DownloadPolicies int32
}

// SegmentView is a segment as the use cases return it: with what targets
// it and, from GetSegment when the counter answers, its estimated recipients.
type SegmentView struct {
	Segment        Segment
	References     References
	EstimatedCount *int32
}

// SegmentChange is the stored change of one update. A nil pointer and an
// unset time leave the column as it is.
type SegmentChange struct {
	Name             *string
	Description      *string
	Type             *SegmentType
	SetCreatedAfter  bool
	CreatedAfter     *time.Time
	SetCreatedBefore bool
	CreatedBefore    *time.Time
	UpdatedAt        time.Time
}

func (c SegmentChange) changed() bool {
	return c.Name != nil || c.Description != nil || c.Type != nil || c.SetCreatedAfter || c.SetCreatedBefore
}

// CampaignUsage is what LockCampaignUsage locked: the segment's campaigns and
// its scheduled and sending delivery runs, by ID.
type CampaignUsage struct {
	CampaignIDs    []string
	DeliveryRunIDs []string
}

// DownloadPolicy is a File attachment's or a Track's restricted download
// policy that grants through a segment.
type DownloadPolicy struct {
	// Owner is the owning document's domain: post, page, work, program_event,
	// or release for a Track.
	Owner   string
	OwnerID string
	// ItemID is the File Block or the Track.
	ItemID   string
	FileID   string
	Audience string
	// SegmentIDs are every segment the policy grants through, sorted.
	SegmentIDs []string
}

// The outcomes of a Store the use cases branch on.
var (
	ErrSegmentNotFound = errors.New("audience segment not found")
	// ErrArchiveStateChanged rejects an archive of an archived segment or a
	// restore of an active one.
	ErrArchiveStateChanged = errors.New("audience segment archive state changed")
	// ErrUnknownReference rejects a filter naming a Member tag or a Member
	// that does not exist.
	ErrUnknownReference = errors.New("audience segment references a missing Member tag or Member")
)
