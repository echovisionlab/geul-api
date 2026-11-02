package audience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const maxArchiveAttempts = 3

var errArchiveRetry = errors.New("audience archive relationship changed while acquiring locks")

// ArchiveSegmentCommand archives a segment.
type ArchiveSegmentCommand struct {
	ID string
}

// ArchiveSegment archives the segment: scheduled campaigns that target it
// return to draft, its scheduled delivery runs are cancelled, and download
// policies stop granting through it. Archiving an archived segment returns
// it unchanged.
func (s *Service) ArchiveSegment(ctx context.Context, command ArchiveSegmentCommand) (SegmentView, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return SegmentView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SegmentView{}, err
	}
	for range maxArchiveAttempts {
		archive := segmentArchive{segmentID: command.ID}
		archived, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[segmentArchive, Store]{
			Lock:   archive.lock,
			Access: freshAdmin(can),
			Change: archive.apply,
			Audit:  segmentArchive.audit,
		})
		if errors.Is(err, errArchiveRetry) {
			continue
		}
		if err != nil {
			return SegmentView{}, err
		}
		return archived.view, nil
	}
	return SegmentView{}, domainerr.Retry("audience segment changed while it was being archived; retry")
}

// segmentArchive is one archive attempt. lock takes the campaign, delivery
// run, segment and exact download-policy locks in the order scheduling and
// policy mutations take them, and apply archives what lock planned.
type segmentArchive struct {
	segmentID       string
	alreadyArchived bool
	policyPlans     []downloadPolicyArchivePlan
	audits          []mutation.Audit
	view            SegmentView
}

func (a *segmentArchive) lock(ctx context.Context, store Store) error {
	archived, err := store.SegmentArchived(ctx, a.segmentID)
	if err != nil {
		return segmentNotFound(err)
	}
	if archived {
		locked, err := store.LockSegment(ctx, a.segmentID)
		if err != nil {
			return segmentNotFound(err)
		}
		if locked.ArchivedAt == nil {
			return errArchiveRetry
		}
		a.alreadyArchived = true
		return nil
	}
	// Preserve the campaign -> delivery run -> audience segment lock order used
	// by scheduling. Download-policy mutations lock audience segments before the
	// exact attachment or Track, so policy relations are locked only after the
	// segment below.
	usage, err := store.LockCampaignUsage(ctx, a.segmentID)
	if err != nil {
		return err
	}
	if err := a.requireUsageUnchanged(ctx, store, usage); err != nil {
		return err
	}
	segment, err := store.LockSegment(ctx, a.segmentID)
	if err != nil {
		return segmentNotFound(err)
	}
	if segment.ArchivedAt != nil {
		a.alreadyArchived = true
		return nil
	}
	if err := a.requireUsageUnchanged(ctx, store, usage); err != nil {
		return err
	}
	policies, err := store.LockDownloadPolicies(ctx, a.segmentID)
	if err != nil {
		return err
	}
	a.policyPlans = make([]downloadPolicyArchivePlan, 0, len(policies))
	for _, policy := range policies {
		plan, ok := newDownloadPolicyArchivePlan(policy, a.segmentID)
		if !ok {
			return errArchiveRetry
		}
		a.policyPlans = append(a.policyPlans, plan)
	}
	return nil
}

// requireUsageUnchanged retries the archive when a campaign or active
// delivery run started or stopped using the segment while the locks were
// taken.
func (a *segmentArchive) requireUsageUnchanged(ctx context.Context, store Store, locked CampaignUsage) error {
	changed, err := store.CampaignUsageChanged(ctx, a.segmentID, locked)
	if err != nil {
		return err
	}
	if changed {
		return errArchiveRetry
	}
	return nil
}

func (a *segmentArchive) apply(ctx context.Context, store Store) (segmentArchive, error) {
	if !a.alreadyArchived {
		if err := a.archive(ctx, store); err != nil {
			return segmentArchive{}, err
		}
	}
	view, err := segmentView(ctx, store, a.segmentID)
	if err != nil {
		return segmentArchive{}, err
	}
	a.view = view
	return *a, nil
}

func (a *segmentArchive) archive(ctx context.Context, store Store) error {
	now := time.Now().UTC()
	draftedCampaignIDs, err := store.ReleaseScheduledCampaigns(ctx, a.segmentID, now)
	if err != nil {
		return err
	}
	if err := store.DetachDownloadPolicies(ctx, a.segmentID); err != nil {
		return err
	}
	if err := store.ArchiveSegment(ctx, a.segmentID, now); err != nil {
		if errors.Is(err, ErrArchiveStateChanged) {
			return errArchiveRetry
		}
		return err
	}
	a.audits, err = archiveAudits(a.segmentID, draftedCampaignIDs, a.policyPlans)
	return err
}

func (a segmentArchive) audit() mutation.Audit {
	if a.alreadyArchived {
		return mutation.Unchanged()
	}
	return mutation.Audits(a.audits...)
}

// RestoreSegmentCommand restores an archived segment.
type RestoreSegmentCommand struct {
	ID string
}

// RestoreSegment makes an archived segment active again.
func (s *Service) RestoreSegment(ctx context.Context, command RestoreSegmentCommand) (SegmentView, error) {
	can, err := policyv1.AudienceSegment.Edit(command.ID)
	if err != nil {
		return SegmentView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SegmentView{}, err
	}
	var segment Segment
	restored, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[SegmentView, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			segment, err = store.LockSegment(ctx, command.ID)
			return segmentNotFound(err)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (SegmentView, error) {
			if segment.ArchivedAt == nil {
				return SegmentView{}, domainerr.Precondition("audience segment is not archived")
			}
			if err := store.RestoreSegment(ctx, segment.ID, time.Now().UTC()); err != nil {
				if errors.Is(err, ErrArchiveStateChanged) {
					return SegmentView{}, domainerr.Precondition("audience segment is not archived")
				}
				return SegmentView{}, err
			}
			return segmentView(ctx, store, segment.ID)
		},
		Audit: func(view SegmentView) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
					metadata, view.Segment.ID, sharedtelemetry.AuditStateArchived, sharedtelemetry.AuditStateActive,
				)
			})
		},
	})
	if err != nil {
		return SegmentView{}, err
	}
	return restored, nil
}

// downloadPolicyArchivePlan is a download policy as the archive locked it
// and the segments it grants through once the archived one is gone.
type downloadPolicyArchivePlan struct {
	policy     DownloadPolicy
	segmentIDs []string
}

// newDownloadPolicyArchivePlan plans removing archivedSegmentID from
// policy, or reports that the policy no longer grants through it.
func newDownloadPolicyArchivePlan(policy DownloadPolicy, archivedSegmentID string) (downloadPolicyArchivePlan, bool) {
	next := make([]string, 0, len(policy.SegmentIDs))
	found := false
	for _, id := range policy.SegmentIDs {
		if id == archivedSegmentID {
			found = true
			continue
		}
		next = append(next, id)
	}
	policy.SegmentIDs = append([]string(nil), policy.SegmentIDs...)
	return downloadPolicyArchivePlan{policy: policy, segmentIDs: next}, found
}

// archiveAudits are the records of one archive, in the order the archive
// applied them: campaigns returned to draft, download policies that lost the
// segment, then the segment.
func archiveAudits(
	segmentID string,
	campaignIDs []string,
	policyPlans []downloadPolicyArchivePlan,
) ([]mutation.Audit, error) {
	audits := make([]mutation.Audit, 0, len(campaignIDs)+len(policyPlans)+1)
	for _, campaignID := range campaignIDs {
		audits = append(audits, mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(
				metadata, campaignID, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft,
			)
		}))
	}
	for _, plan := range policyPlans {
		previous := append([]string(nil), plan.policy.SegmentIDs...)
		next := append([]string(nil), plan.segmentIDs...)
		action, builder, err := downloadPolicyArchiveAudit(plan.policy, previous, next)
		if err != nil {
			return nil, err
		}
		audits = append(audits, mutation.RequestAudit(action, builder))
	}
	return append(audits, mutation.RequestAudit(sharedtelemetry.AuditAudienceSegmentUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewAudienceSegmentLifecycleUpdatedAuditRecord(
			metadata, segmentID, sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateArchived,
		)
	})), nil
}

func downloadPolicyArchiveAudit(
	policy DownloadPolicy,
	previousSegmentIDs []string,
	segmentIDs []string,
) (
	sharedtelemetry.AuditAction,
	func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error),
	error,
) {
	previousAudience := sharedtelemetry.AuditState(policy.Audience)
	newAudience := previousAudience
	switch policy.Owner {
	case "post":
		return sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "page":
		return sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "work":
		return sharedtelemetry.AuditWorkUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "program_event":
		return sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	case "release":
		return sharedtelemetry.AuditReleaseUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousAudience, newAudience, previousSegmentIDs, segmentIDs)
		}, nil
	default:
		return "", nil, fmt.Errorf("unsupported download policy owner %q", policy.Owner)
	}
}
