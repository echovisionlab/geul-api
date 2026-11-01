package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

type CampaignDeliveryRecipientJob struct {
	Recipient DeliveryRecipient
	Run       DeliveryRun
}

const CampaignDeliveryRecipientClaimLease = 10 * time.Minute

var (
	ErrCampaignDeliveryRecipientClaimed = errors.New("campaign delivery recipient has an active claim")
	ErrCampaignDeliveryClaimLost        = errors.New("campaign delivery claim is no longer owned")
)

func campaignDeliveryRecipientTerminalStatus(status string) bool {
	switch status {
	case CampaignDeliveryRecipientStatusSent,
		CampaignDeliveryRecipientStatusDelivered,
		CampaignDeliveryRecipientStatusSkipped,
		CampaignDeliveryRecipientStatusPermanentFailed,
		CampaignDeliveryRecipientStatusBlocked,
		CampaignDeliveryRecipientStatusSuppressed,
		CampaignDeliveryRecipientStatusBounced,
		CampaignDeliveryRecipientStatusComplained:
		return true
	default:
		return false
	}
}

func campaignDeliveryRecipientStatusFinalizesRun(status string) bool {
	return campaignDeliveryRecipientTerminalStatus(status)
}

// MaterializeCampaignDeliveryRun freezes a sending run's recipients in tx,
// the delivery worker's transaction.
func MaterializeCampaignDeliveryRun(
	ctx context.Context,
	joined Joined,
	runID string,
) error {
	if strings.TrimSpace(runID) == "" {
		return domainerr.Required("run_id")
	}

	tx := joined.Store
	locked, _, err := tx.LockDeliveryRun(ctx, runID, false)
	if err != nil {
		return err
	}
	run := locked.Run
	if run.Status != CampaignDeliveryRunStatusSending {
		return nil
	}
	target, err := loadCampaignDeliveryTarget(ctx, tx, run)
	if err != nil {
		return err
	}

	if err := tx.MaterializeRecipients(ctx, run, target); err != nil {
		return err
	}

	total, err := tx.RecipientCount(ctx, runID)
	if err != nil {
		return err
	}
	return tx.SetTargetCount(ctx, runID, int(total))
}

func ListPendingCampaignDeliveryRecipients(
	ctx context.Context,
	joined Joined,
	runID string,
	afterID string,
	limit int,
) ([]CampaignDeliveryRecipientJob, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, domainerr.Required("run_id")
	}
	if limit <= 0 {
		limit = 100
	}

	run, err := joined.Store.FindRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	recipients, err := joined.Store.PendingRecipients(ctx, runID, strings.TrimSpace(afterID), limit, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	jobs := make([]CampaignDeliveryRecipientJob, 0, len(recipients))
	for _, recipient := range recipients {
		jobs = append(jobs, CampaignDeliveryRecipientJob{Recipient: recipient, Run: run})
	}
	return jobs, nil
}

func HasPendingCampaignDeliveryRecipients(ctx context.Context, joined Joined, runID string) (bool, error) {
	return joined.Store.HasPendingRecipients(ctx, strings.TrimSpace(runID))
}

func CampaignDeliveryRecipientNeedsDelivery(
	ctx context.Context,
	joined Joined,
	recipientID string,
) (bool, error) {
	recipientID = strings.TrimSpace(recipientID)
	if recipientID == "" {
		return true, nil
	}

	status, err := joined.Store.RecipientStatus(ctx, recipientID)
	if err != nil {
		return false, err
	}

	if status == CampaignDeliveryRecipientStatusPending {
		return true, nil
	}
	if campaignDeliveryRecipientTerminalStatus(status) {
		return false, nil
	}
	return false, fmt.Errorf("campaign delivery recipient has unsupported status %q", status)
}

// ClaimCampaignDeliveryRecipient atomically reserves a pending recipient for
// one worker. The claim is reclaimable after its lease expires; the token
// fences terminal writes and releases from an earlier owner.
func ClaimCampaignDeliveryRecipient(
	ctx context.Context,
	joined Joined,
	recipientID string,
	claimID string,
) (bool, error) {
	recipientID = strings.TrimSpace(recipientID)
	claimID = strings.TrimSpace(claimID)
	if recipientID == "" {
		return false, fmt.Errorf("campaign delivery recipient id is required")
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return false, fmt.Errorf("campaign delivery claim id must be a UUID: %w", err)
	}
	tx := joined.Store

	identity, err := tx.RecipientIdentity(ctx, recipientID)
	if err != nil {
		return false, err
	}

	locked, _, err := tx.LockDeliveryRun(ctx, identity.RunID, false)
	if err != nil {
		return false, err
	}

	recipient, err := tx.LockClaimRecipient(ctx, recipientID)
	if err != nil {
		return false, err
	}

	if recipient.RunID != locked.Run.ID {
		return false, fmt.Errorf("campaign delivery recipient relationship changed while acquiring locks")
	}
	// Start the lease only after all ordered locks have been acquired. A
	// worker may otherwise wait behind a slow transaction and receive a
	// claim whose lease is already partly or fully consumed at commit.
	now := time.Now().UTC()
	expiresAt := now.Add(CampaignDeliveryRecipientClaimLease)
	if campaignDeliveryRecipientTerminalStatus(recipient.Status) {
		return false, nil
	}
	if recipient.Status != CampaignDeliveryRecipientStatusPending {
		return false, fmt.Errorf("campaign delivery recipient has unsupported status %q", recipient.Status)
	}
	if recipient.DeliveryClaimID != nil && recipient.DeliveryClaimExpiresAt != nil &&
		recipient.DeliveryClaimExpiresAt.After(now) {
		return false, nil
	}
	return tx.ClaimRecipient(ctx, recipient, claimID, now, expiresAt)
}

// ReleaseCampaignDeliveryRecipientClaim releases only the exact claim owner.
// A stale worker cannot clear a newer worker's lease.
func ReleaseCampaignDeliveryRecipientClaim(
	ctx context.Context,
	joined Joined,
	recipientID string,
	claimID string,
) error {
	recipientID = strings.TrimSpace(recipientID)
	claimID = strings.TrimSpace(claimID)
	if recipientID == "" || claimID == "" {
		return fmt.Errorf("campaign delivery recipient and claim ids are required")
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return fmt.Errorf("campaign delivery claim id must be a UUID: %w", err)
	}
	tx := joined.Store

	identity, err := tx.RecipientIdentity(ctx, recipientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	locked, _, err := tx.LockDeliveryRun(ctx, identity.RunID, false)
	if err != nil {
		return err
	}

	recipient, err := tx.LockReleaseRecipient(ctx, recipientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}

	if recipient.RunID != locked.Run.ID {
		return fmt.Errorf("campaign delivery recipient relationship changed while acquiring locks")
	}
	if recipient.Status != CampaignDeliveryRecipientStatusPending ||
		recipient.DeliveryClaimID == nil || *recipient.DeliveryClaimID != claimID {
		return nil
	}
	return tx.ReleaseRecipientClaim(ctx, recipient, claimID)
}

// MarkCampaignDeliveryRecipientResultWithAudit finalizes the authoritative
// recipient result. Only an actual Campaign terminal transition produces the
// backend Audit record; per-recipient outcomes deliberately never do.
func MarkCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	joined Joined,
	recipientID string,
	status string,
	providerMessageID string,
	errorType string,
	metrics CampaignDeliveryMetrics,
) error {
	return markCampaignDeliveryRecipientResultWithAudit(
		ctx, joined, recipientID, "", status, providerMessageID, errorType, metrics, false,
	)
}

// MarkClaimedCampaignDeliveryRecipientResultWithAudit finalizes the
// authoritative recipient result only while the supplied lease token is still
// current and unexpired.
func MarkClaimedCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	joined Joined,
	recipientID string,
	claimID string,
	status string,
	providerMessageID string,
	errorType string,
	metrics CampaignDeliveryMetrics,
) error {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return fmt.Errorf("campaign delivery claim id is required")
	}
	if _, err := uuid.Parse(claimID); err != nil {
		return fmt.Errorf("campaign delivery claim id must be a UUID: %w", err)
	}
	return markCampaignDeliveryRecipientResultWithAudit(
		ctx, joined, recipientID, claimID, status, providerMessageID, errorType, metrics, true,
	)
}

// recipientResult is one terminal result a delivery worker reports.
type recipientResult struct {
	recipientID       string
	claimID           string
	status            string
	providerMessageID string
	errorType         string
	requireClaim      bool
}

// validate rejects a result that is not terminal or whose provider message
// does not match its outcome.
func (r recipientResult) validate() error {
	if !campaignDeliveryRecipientStatusFinalizesRun(r.status) {
		return fmt.Errorf("campaign delivery result status %q is not terminal", r.status)
	}
	accepted := r.status == CampaignDeliveryRecipientStatusSent || r.status == CampaignDeliveryRecipientStatusDelivered
	if accepted && r.providerMessageID == "" {
		return fmt.Errorf("accepted campaign delivery requires provider message id")
	}
	if !accepted && r.providerMessageID != "" {
		return fmt.Errorf("failed campaign delivery cannot carry provider message id")
	}
	return nil
}

// requireWritable reports whether the locked recipient still takes this
// result: a terminal recipient keeps its result, and the delivery claim
// must be this worker's live lease, or for an unclaimed result absent or
// expired.
func (r recipientResult) requireWritable(recipient DeliveryRecipient, now time.Time) (bool, error) {
	if campaignDeliveryRecipientTerminalStatus(recipient.Status) {
		return false, nil
	}
	if recipient.Status != CampaignDeliveryRecipientStatusPending {
		return false, fmt.Errorf("campaign delivery recipient is not pending")
	}
	claimLive := recipient.DeliveryClaimID != nil && recipient.DeliveryClaimExpiresAt != nil && recipient.DeliveryClaimExpiresAt.After(now)
	if r.requireClaim && (!claimLive || *recipient.DeliveryClaimID != r.claimID) {
		return false, ErrCampaignDeliveryClaimLost
	}
	if !r.requireClaim && claimLive {
		return false, ErrCampaignDeliveryRecipientClaimed
	}
	return true, nil
}

// store writes the result over the still-pending recipient of the locked
// run, under the same claim condition requireWritable checked.
func (r recipientResult) store(ctx context.Context, tx Store, runID string, now time.Time) error {

	return tx.StoreRecipientResult(ctx, RecipientResult{RecipientID: r.recipientID, ClaimID: r.claimID, Status: r.status, ProviderMessageID: r.providerMessageID, ErrorType: r.errorType, RequireClaim: r.requireClaim}, runID, now)
}

// lockResultRecipient locks the recipient's run and then the recipient.
func lockResultRecipient(ctx context.Context, tx Store, recipientID string) (LockedDeliveryRun, DeliveryRecipient, error) {

	identity, err := tx.RecipientIdentity(ctx, recipientID)
	if err != nil {
		return LockedDeliveryRun{}, DeliveryRecipient{}, err
	}
	locked, _, err := tx.LockDeliveryRun(ctx, identity.RunID, false)
	if err != nil {
		return LockedDeliveryRun{}, DeliveryRecipient{}, err
	}
	recipient, err := tx.LockResultRecipient(ctx, recipientID)
	if err != nil {
		return LockedDeliveryRun{}, DeliveryRecipient{}, err
	}
	if recipient.RunID != locked.Run.ID {
		return LockedDeliveryRun{}, DeliveryRecipient{}, fmt.Errorf("campaign delivery recipient relationship changed while acquiring locks")
	}
	return locked, recipient, nil
}

func markCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	joined Joined,
	recipientID string,
	claimID string,
	status string,
	providerMessageID string,
	errorType string,
	metrics CampaignDeliveryMetrics,
	requireClaim bool,
) error {
	result := recipientResult{
		recipientID: strings.TrimSpace(recipientID), claimID: claimID, status: status,
		providerMessageID: strings.TrimSpace(providerMessageID), errorType: strings.TrimSpace(errorType),
		requireClaim: requireClaim,
	}
	if result.recipientID == "" {
		return nil
	}
	if err := result.validate(); err != nil {
		return err
	}
	tx := joined.Store
	locked, recipient, err := lockResultRecipient(ctx, tx, result.recipientID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	writable, err := result.requireWritable(recipient, now)
	if err != nil || !writable {
		return err
	}
	if err := result.store(ctx, tx, locked.Run.ID, now); err != nil {
		return err
	}
	return finalizeLockedEmailDeliveryRun(ctx, joined, &locked, metrics)

}

func finalizeLockedEmailDeliveryRun(
	ctx context.Context,
	joined Joined,
	locked *LockedDeliveryRun,
	metrics CampaignDeliveryMetrics,
) error {
	tx := joined.Store
	run := &locked.Run
	if run.Status != CampaignDeliveryRunStatusSending {
		return nil
	}

	counts, err := tx.CompletionCounts(ctx, run.ID)
	if err != nil {
		return err
	}

	run.SentCount = int(counts.Sent + counts.Delivered)
	run.SkippedCount = int(counts.Skipped)
	run.FailedCount = int(counts.PermanentFail + counts.Bounced + counts.Complained)
	run.BlockedCount = int(counts.Blocked)
	run.SuppressedCount = int(counts.Suppressed)

	decision := decideEmailDeliveryCompletion(counts, run.TargetCount, run.RunKind)
	if !decision.Complete {
		return tx.SaveRun(ctx, run)
	}

	now := time.Now()
	run.CompletedAt = &now
	run.Status = decision.RunStatus
	if metrics != nil {
		metrics.RecordRunDuration(ctx, *run, now)
	}
	if err := tx.SaveRun(ctx, run); err != nil {
		return err
	}
	if run.RunKind != EmailDeliveryRunKindCampaign {
		return nil
	}
	campaignID := strings.TrimSpace(ptrStringValue(run.CampaignID))
	if campaignID == "" || decision.CampaignStatus == "" {
		return nil
	}
	if locked.Campaign == nil || locked.Campaign.ID != campaignID {
		return fmt.Errorf("campaign delivery run campaign lock is missing")
	}
	previousStatus := locked.Campaign.Status
	locked.Campaign.Status = decision.CampaignStatus
	if decision.RunStatus == CampaignDeliveryRunStatusSent {
		locked.Campaign.SentAt = &now
		locked.Campaign.SentCount = int(counts.Sent + counts.Delivered)
	}
	if err := tx.SaveCampaign(ctx, locked.Campaign); err != nil {
		return err
	}
	return appendCampaignTerminalStatusAudit(ctx, joined, campaignID, campaignAuditState(previousStatus), campaignAuditState(decision.CampaignStatus))
}
