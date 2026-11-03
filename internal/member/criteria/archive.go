package criteria

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

var errArchiveRetry = errors.New("criteria archive relationship changed while acquiring locks")

// ArchiveCriteriaCommand archives a criteria.
type ArchiveCriteriaCommand struct {
	ID string
}

// ArchiveCriteria archives the criteria: scheduled campaigns that target it
// return to draft, its scheduled delivery runs are cancelled, and download
// policies stop granting through it. Archiving an archived criteria returns
// it unchanged.
func (s *Service) ArchiveCriteria(ctx context.Context, command ArchiveCriteriaCommand) (CriteriaView, error) {
	can, err := policyv1.MemberCriteria.Edit(command.ID)
	if err != nil {
		return CriteriaView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return CriteriaView{}, err
	}
	for range maxArchiveAttempts {
		archive := criteriaArchive{criteriaID: command.ID}
		archived, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[criteriaArchive, Store]{
			Lock:   archive.lock,
			Access: freshAdmin(can),
			Change: archive.apply,
			Audit:  criteriaArchive.audit,
		})
		if errors.Is(err, errArchiveRetry) {
			continue
		}
		if err != nil {
			return CriteriaView{}, err
		}
		return archived.view, nil
	}
	return CriteriaView{}, domainerr.Retry("Member criteria changed while it was being archived; retry")
}

// criteriaArchive is one archive attempt. lock takes the campaign, delivery
// run, criteria and exact download-policy locks in the order scheduling and
// policy mutations take them, and apply archives what lock planned.
type criteriaArchive struct {
	criteriaID      string
	alreadyArchived bool
	policyPlans     []downloadPolicyArchivePlan
	audits          []mutation.Audit
	view            CriteriaView
}

func (a *criteriaArchive) lock(ctx context.Context, store Store) error {
	archived, err := store.CriteriaArchived(ctx, a.criteriaID)
	if err != nil {
		return criteriaNotFound(err)
	}
	if archived {
		locked, err := store.LockCriteria(ctx, a.criteriaID)
		if err != nil {
			return criteriaNotFound(err)
		}
		if locked.ArchivedAt == nil {
			return errArchiveRetry
		}
		a.alreadyArchived = true
		return nil
	}
	// Preserve the campaign -> delivery run -> Member criteria lock order used
	// by scheduling. Download-policy mutations lock Member criteria before the
	// exact attachment or Track, so policy relations are locked only after the
	// criteria below.
	usage, err := store.LockCampaignUsage(ctx, a.criteriaID)
	if err != nil {
		return err
	}
	if err := a.requireUsageUnchanged(ctx, store, usage); err != nil {
		return err
	}
	criteria, err := store.LockCriteria(ctx, a.criteriaID)
	if err != nil {
		return criteriaNotFound(err)
	}
	if criteria.ArchivedAt != nil {
		a.alreadyArchived = true
		return nil
	}
	if err := a.requireUsageUnchanged(ctx, store, usage); err != nil {
		return err
	}
	policies, err := store.LockDownloadPolicies(ctx, a.criteriaID)
	if err != nil {
		return err
	}
	a.policyPlans = make([]downloadPolicyArchivePlan, 0, len(policies))
	for _, policy := range policies {
		plan, ok := newDownloadPolicyArchivePlan(policy, a.criteriaID)
		if !ok {
			return errArchiveRetry
		}
		a.policyPlans = append(a.policyPlans, plan)
	}
	return nil
}

// requireUsageUnchanged retries the archive when a campaign or active
// delivery run started or stopped using the criteria while the locks were
// taken.
func (a *criteriaArchive) requireUsageUnchanged(ctx context.Context, store Store, locked CampaignUsage) error {
	changed, err := store.CampaignUsageChanged(ctx, a.criteriaID, locked)
	if err != nil {
		return err
	}
	if changed {
		return errArchiveRetry
	}
	return nil
}

func (a *criteriaArchive) apply(ctx context.Context, store Store) (criteriaArchive, error) {
	if !a.alreadyArchived {
		if err := a.archive(ctx, store); err != nil {
			return criteriaArchive{}, err
		}
	}
	view, err := criteriaView(ctx, store, a.criteriaID)
	if err != nil {
		return criteriaArchive{}, err
	}
	a.view = view
	return *a, nil
}

func (a *criteriaArchive) archive(ctx context.Context, store Store) error {
	now := time.Now().UTC()
	draftedCampaignIDs, err := store.ReleaseScheduledCampaigns(ctx, a.criteriaID, now)
	if err != nil {
		return err
	}
	if err := store.DetachDownloadPolicies(ctx, a.criteriaID); err != nil {
		return err
	}
	if err := store.ArchiveCriteria(ctx, a.criteriaID, now); err != nil {
		if errors.Is(err, ErrArchiveStateChanged) {
			return errArchiveRetry
		}
		return err
	}
	a.audits, err = archiveAudits(a.criteriaID, draftedCampaignIDs, a.policyPlans)
	return err
}

func (a criteriaArchive) audit() mutation.Audit {
	if a.alreadyArchived {
		return mutation.Unchanged()
	}
	return mutation.Audits(a.audits...)
}

// RestoreCriteriaCommand restores an archived criteria.
type RestoreCriteriaCommand struct {
	ID string
}

// RestoreCriteria makes an archived criteria active again.
func (s *Service) RestoreCriteria(ctx context.Context, command RestoreCriteriaCommand) (CriteriaView, error) {
	can, err := policyv1.MemberCriteria.Edit(command.ID)
	if err != nil {
		return CriteriaView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return CriteriaView{}, err
	}
	var criteria Criteria
	restored, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[CriteriaView, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			criteria, err = store.LockCriteria(ctx, command.ID)
			return criteriaNotFound(err)
		},
		Access: freshAdmin(can),
		Change: func(ctx context.Context, store Store) (CriteriaView, error) {
			if criteria.ArchivedAt == nil {
				return CriteriaView{}, domainerr.Precondition("Member criteria is not archived")
			}
			if err := store.RestoreCriteria(ctx, criteria.ID, time.Now().UTC()); err != nil {
				if errors.Is(err, ErrArchiveStateChanged) {
					return CriteriaView{}, domainerr.Precondition("Member criteria is not archived")
				}
				return CriteriaView{}, err
			}
			return criteriaView(ctx, store, criteria.ID)
		},
		Audit: func(view CriteriaView) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberCriteriaUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberCriteriaLifecycleUpdatedAuditRecord(
					metadata, view.Criteria.ID, sharedtelemetry.AuditStateArchived, sharedtelemetry.AuditStateActive,
				)
			})
		},
	})
	if err != nil {
		return CriteriaView{}, err
	}
	return restored, nil
}

// downloadPolicyArchivePlan is a download policy as the archive locked it
// and the Member criteria it grants through once the archived one is gone.
type downloadPolicyArchivePlan struct {
	policy      DownloadPolicy
	criteriaIDs []string
}

// newDownloadPolicyArchivePlan plans removing archivedCriteriaID from
// policy, or reports that the policy no longer grants through it.
func newDownloadPolicyArchivePlan(policy DownloadPolicy, archivedCriteriaID string) (downloadPolicyArchivePlan, bool) {
	next := make([]string, 0, len(policy.CriteriaIDs))
	found := false
	for _, id := range policy.CriteriaIDs {
		if id == archivedCriteriaID {
			found = true
			continue
		}
		next = append(next, id)
	}
	policy.CriteriaIDs = append([]string(nil), policy.CriteriaIDs...)
	return downloadPolicyArchivePlan{policy: policy, criteriaIDs: next}, found
}

// archiveAudits are the records of one archive, in the order the archive
// applied them: campaigns returned to draft, download policies that lost the
// criteria, then the criteria.
func archiveAudits(
	criteriaID string,
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
		previous := append([]string(nil), plan.policy.CriteriaIDs...)
		next := append([]string(nil), plan.criteriaIDs...)
		action, builder, err := downloadPolicyArchiveAudit(plan.policy, previous, next)
		if err != nil {
			return nil, err
		}
		audits = append(audits, mutation.RequestAudit(action, builder))
	}
	return append(audits, mutation.RequestAudit(sharedtelemetry.AuditMemberCriteriaUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewMemberCriteriaLifecycleUpdatedAuditRecord(
			metadata, criteriaID, sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateArchived,
		)
	})), nil
}

func downloadPolicyArchiveAudit(
	policy DownloadPolicy,
	previousCriteriaIDs []string,
	criteriaIDs []string,
) (
	sharedtelemetry.AuditAction,
	func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error),
	error,
) {
	previousCriteria := sharedtelemetry.AuditState(policy.Criteria)
	newCriteria := previousCriteria
	switch policy.Owner {
	case "post":
		return sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousCriteria, newCriteria, previousCriteriaIDs, criteriaIDs)
		}, nil
	case "page":
		return sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousCriteria, newCriteria, previousCriteriaIDs, criteriaIDs)
		}, nil
	case "work":
		return sharedtelemetry.AuditWorkUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousCriteria, newCriteria, previousCriteriaIDs, criteriaIDs)
		}, nil
	case "program_event":
		return sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventFileBlockDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousCriteria, newCriteria, previousCriteriaIDs, criteriaIDs)
		}, nil
	case "release":
		return sharedtelemetry.AuditReleaseUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackDownloadPolicyAuditRecord(metadata, policy.OwnerID, policy.ItemID, policy.FileID, previousCriteria, newCriteria, previousCriteriaIDs, criteriaIDs)
		}, nil
	default:
		return "", nil, fmt.Errorf("unsupported download policy owner %q", policy.Owner)
	}
}
