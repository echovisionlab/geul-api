package criteria

import (
	"context"
	"time"
)

// Store reads and writes Member criteria within one transaction or read snapshot
// of its unit of work (criteria/postgres.NewUnits). It also reaches what
// another domain owns and a criteria change must see in the same
// transaction: the campaigns and delivery runs that target a criteria
// (campaign.CriteriaUsage), the download policies that grant through it, and
// Member eligibility.
type Store interface {
	// Criteria reads a criteria with its filter, or ErrCriteriaNotFound.
	Criteria(ctx context.Context, id string) (Criteria, error)
	// CriteriaArchived reads, without a lock, whether a criteria is archived,
	// or ErrCriteriaNotFound.
	CriteriaArchived(ctx context.Context, id string) (bool, error)
	// LockCriteria locks a criteria's row and reads it with its filter, or
	// ErrCriteriaNotFound.
	LockCriteria(ctx context.Context, id string) (Criteria, error)
	// ListCriteriaSet reads one page of Member criteria with their filters, active
	// ones only unless includeArchived, and the total the filters select.
	ListCriteriaSet(ctx context.Context, includeArchived bool, page Page) ([]Criteria, int64, error)
	// ListAuthenticatedAccessCriteriaSet reads one page of the active member-tag
	// and member-filter Member criteria, without their filters, and the total.
	ListAuthenticatedAccessCriteriaSet(ctx context.Context, page Page) ([]Criteria, int64, error)
	// CriteriaReferences counts what targets each criteria; a criteria nothing
	// targets is absent.
	CriteriaReferences(ctx context.Context, ids []string) (map[string]References, error)

	// InsertCriteria stores a new criteria's row and returns it as stored,
	// with its generated ID and timestamps. The filter relations are
	// ReplaceConfig's.
	InsertCriteria(ctx context.Context, criteria Criteria) (Criteria, error)
	// UpdateCriteria applies change to a criteria's row.
	UpdateCriteria(ctx context.Context, id string, change CriteriaChange) error
	// ReplaceConfig replaces a criteria's Member tag, role and excluded
	// Member relations, or answers ErrUnknownReference for a Member tag or
	// Member that does not exist.
	ReplaceConfig(ctx context.Context, id string, config Config) error
	// ArchiveCriteria archives an active criteria at at, or answers
	// ErrArchiveStateChanged.
	ArchiveCriteria(ctx context.Context, id string, at time.Time) error
	// RestoreCriteria makes an archived criteria active at at, or answers
	// ErrArchiveStateChanged.
	RestoreCriteria(ctx context.Context, id string, at time.Time) error
	// EligibleMemberIDs returns the given Members that are onboarded and have
	// an exact Identity link.
	EligibleMemberIDs(ctx context.Context, memberIDs []string) ([]string, error)

	// CriteriaInActiveDelivery reports whether a scheduled or sending
	// campaign, and whether a scheduled or sending delivery run, targets the
	// criteria.
	CriteriaInActiveDelivery(ctx context.Context, id string) (campaign, deliveryRun bool, err error)
	// LockCampaignUsage locks the criteria's campaigns, then its scheduled and
	// sending delivery runs, in the order scheduling locks them.
	LockCampaignUsage(ctx context.Context, id string) (CampaignUsage, error)
	// CampaignUsageChanged reports whether the criteria's campaigns or active
	// delivery runs differ from the locked ones.
	CampaignUsageChanged(ctx context.Context, id string, locked CampaignUsage) (bool, error)
	// ReleaseScheduledCampaigns cancels the criteria's scheduled delivery runs
	// and returns its scheduled campaigns to draft, returning their IDs.
	ReleaseScheduledCampaigns(ctx context.Context, id string, at time.Time) (draftedCampaignIDs []string, err error)

	// GrantsDownloadAccess reports whether a download policy grants through
	// the criteria.
	GrantsDownloadAccess(ctx context.Context, id string) (bool, error)
	// LockDownloadPolicies locks the File attachments, then the Tracks, whose
	// download policy grants through the criteria, and returns those policies
	// in that order.
	LockDownloadPolicies(ctx context.Context, id string) ([]DownloadPolicy, error)
	// DetachDownloadPolicies removes the criteria from every download policy.
	DetachDownloadPolicies(ctx context.Context, id string) error
}

// RecipientCounter supplies the authoritative recipient estimate for an
// Member criteria.
type RecipientCounter interface {
	Count(context.Context, Criteria) (int64, error)
}
