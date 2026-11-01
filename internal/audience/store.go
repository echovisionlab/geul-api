package audience

import (
	"context"
	"time"
)

// Store reads and writes segments within one transaction or read snapshot
// of its unit of work (audience/postgres.NewUnits). It also reaches what
// another domain owns and a segment change must see in the same
// transaction: the campaigns and delivery runs that target a segment
// (campaign.SegmentUsage), the download policies that grant through it, and
// Member eligibility.
type Store interface {
	// Segment reads a segment with its filter, or ErrSegmentNotFound.
	Segment(ctx context.Context, id string) (Segment, error)
	// SegmentArchived reads, without a lock, whether a segment is archived,
	// or ErrSegmentNotFound.
	SegmentArchived(ctx context.Context, id string) (bool, error)
	// LockSegment locks a segment's row and reads it with its filter, or
	// ErrSegmentNotFound.
	LockSegment(ctx context.Context, id string) (Segment, error)
	// ListSegments reads one page of segments with their filters, active
	// ones only unless includeArchived, and the total the filters select.
	ListSegments(ctx context.Context, includeArchived bool, page Page) ([]Segment, int64, error)
	// ListAuthenticatedAccessSegments reads one page of the active member-tag
	// and member-filter segments, without their filters, and the total.
	ListAuthenticatedAccessSegments(ctx context.Context, page Page) ([]Segment, int64, error)
	// SegmentReferences counts what targets each segment; a segment nothing
	// targets is absent.
	SegmentReferences(ctx context.Context, ids []string) (map[string]References, error)

	// InsertSegment stores a new segment's row and returns it as stored,
	// with its generated ID and timestamps. The filter relations are
	// ReplaceConfig's.
	InsertSegment(ctx context.Context, segment Segment) (Segment, error)
	// UpdateSegment applies change to a segment's row.
	UpdateSegment(ctx context.Context, id string, change SegmentChange) error
	// ReplaceConfig replaces a segment's Member tag, role and excluded
	// Member relations, or answers ErrUnknownReference for a Member tag or
	// Member that does not exist.
	ReplaceConfig(ctx context.Context, id string, config Config) error
	// ArchiveSegment archives an active segment at at, or answers
	// ErrArchiveStateChanged.
	ArchiveSegment(ctx context.Context, id string, at time.Time) error
	// RestoreSegment makes an archived segment active at at, or answers
	// ErrArchiveStateChanged.
	RestoreSegment(ctx context.Context, id string, at time.Time) error
	// EligibleMemberIDs returns the given Members that are onboarded and have
	// an exact Identity link.
	EligibleMemberIDs(ctx context.Context, memberIDs []string) ([]string, error)

	// SegmentInActiveDelivery reports whether a scheduled or sending
	// campaign, and whether a scheduled or sending delivery run, targets the
	// segment.
	SegmentInActiveDelivery(ctx context.Context, id string) (campaign, deliveryRun bool, err error)
	// LockCampaignUsage locks the segment's campaigns, then its scheduled and
	// sending delivery runs, in the order scheduling locks them.
	LockCampaignUsage(ctx context.Context, id string) (CampaignUsage, error)
	// CampaignUsageChanged reports whether the segment's campaigns or active
	// delivery runs differ from the locked ones.
	CampaignUsageChanged(ctx context.Context, id string, locked CampaignUsage) (bool, error)
	// ReleaseScheduledCampaigns cancels the segment's scheduled delivery runs
	// and returns its scheduled campaigns to draft, returning their IDs.
	ReleaseScheduledCampaigns(ctx context.Context, id string, at time.Time) (draftedCampaignIDs []string, err error)

	// GrantsDownloadAccess reports whether a download policy grants through
	// the segment.
	GrantsDownloadAccess(ctx context.Context, id string) (bool, error)
	// LockDownloadPolicies locks the File attachments, then the Tracks, whose
	// download policy grants through the segment, and returns those policies
	// in that order.
	LockDownloadPolicies(ctx context.Context, id string) ([]DownloadPolicy, error)
	// DetachDownloadPolicies removes the segment from every download policy.
	DetachDownloadPolicies(ctx context.Context, id string) error
}

// RecipientCounter supplies the authoritative recipient estimate for an
// audience segment.
type RecipientCounter interface {
	Count(context.Context, Segment) (int64, error)
}
