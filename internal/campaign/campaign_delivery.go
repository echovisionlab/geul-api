package campaign

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"

	"github.com/echovisionlab/geul-api/internal/dependencycheck"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	EmailDeliveryRunKindCampaign    = "campaign"
	EmailDeliveryRunKindLegalNotice = "legal_notice"

	CampaignDeliveryRunStatusScheduled = "scheduled"
	CampaignDeliveryRunStatusSending   = "sending"
	CampaignDeliveryRunStatusSent      = "sent"
	CampaignDeliveryRunStatusFailed    = "failed"
	CampaignDeliveryRunStatusCancelled = "cancelled"
	CampaignDeliveryRunStatusSkipped   = "skipped"

	CampaignDeliveryRecipientStatusPending         = "pending"
	CampaignDeliveryRecipientStatusSent            = "sent"
	CampaignDeliveryRecipientStatusDelivered       = "delivered"
	CampaignDeliveryRecipientStatusSkipped         = "skipped"
	CampaignDeliveryRecipientStatusPermanentFailed = "permanent_failed"
	CampaignDeliveryRecipientStatusBlocked         = "blocked"
	CampaignDeliveryRecipientStatusSuppressed      = "suppressed"
	CampaignDeliveryRecipientStatusBounced         = "bounced"
	CampaignDeliveryRecipientStatusComplained      = "complained"
)

type DeliveryCompletionCounts struct {
	Total         int64
	Pending       int64
	Sent          int64
	Delivered     int64
	Skipped       int64
	PermanentFail int64
	Blocked       int64
	Suppressed    int64
	Bounced       int64
	Complained    int64
}

type campaignDeliveryCompletionDecision struct {
	Complete       bool
	RunStatus      string
	CampaignStatus CampaignStatus
}

func decideEmailDeliveryCompletion(counts DeliveryCompletionCounts, targetCount int, runKind string) campaignDeliveryCompletionDecision {
	if counts.Total < int64(targetCount) ||
		counts.Pending > 0 {
		return campaignDeliveryCompletionDecision{}
	}

	if counts.Sent+counts.Delivered > 0 {
		return campaignDeliveryCompletionDecision{
			Complete:       true,
			RunStatus:      CampaignDeliveryRunStatusSent,
			CampaignStatus: campaignStatusForCompletedRun(CampaignDeliveryRunStatusSent, runKind),
		}
	}

	if runKind == EmailDeliveryRunKindLegalNotice && targetCount == 0 && counts.Total == 0 {
		return campaignDeliveryCompletionDecision{
			Complete:  true,
			RunStatus: CampaignDeliveryRunStatusSkipped,
		}
	}

	return campaignDeliveryCompletionDecision{
		Complete:       true,
		RunStatus:      CampaignDeliveryRunStatusFailed,
		CampaignStatus: campaignStatusForCompletedRun(CampaignDeliveryRunStatusFailed, runKind),
	}
}

func campaignStatusForCompletedRun(runStatus string, runKind string) CampaignStatus {
	if runKind != EmailDeliveryRunKindCampaign {
		return ""
	}
	switch runStatus {
	case CampaignDeliveryRunStatusSent:
		return CampaignStatusSent
	case CampaignDeliveryRunStatusFailed:
		return CampaignStatusFailed
	default:
		return ""
	}
}

type CampaignBulkPublisher interface {
	PublishSendBulkEmail(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error
}

// CampaignDeliveryDispatcher moves due delivery runs to sending and resumes
// active ones. The delivery worker has no request principal to run
// mutation.Run for, so the dispatcher opens no transaction: its caller
// (internal/adapters/campaign) passes a Joined Store for each read and
// transaction. Each write commits in the caller's transaction.
type CampaignDeliveryDispatcher struct {
	publisher CampaignBulkPublisher
	now       func() time.Time
}

func NewCampaignDeliveryDispatcher(publisher CampaignBulkPublisher, now func() time.Time) *CampaignDeliveryDispatcher {
	dependencycheck.MustNotNil(publisher, "publisher")
	dependencycheck.MustNotNil(now, "clock")
	return &CampaignDeliveryDispatcher{publisher: publisher, now: now}
}

// enqueueStartedCampaignDeliveryRun records the first email.campaign command
// beside an immediate-send run. The run has already captured its immutable
// definition; the worker performs recipient materialization after this durable
// handoff commits.
func enqueueStartedCampaignDeliveryRun(
	ctx context.Context,
	tx Store,
	run *DeliveryRun,
	now time.Time,
) error {
	if run == nil || strings.TrimSpace(run.ID) == "" {
		return fmt.Errorf("campaign delivery run is required")
	}
	if run.Status != CampaignDeliveryRunStatusScheduled {
		return fmt.Errorf("campaign delivery run is not ready for dispatch")
	}
	run.Status = CampaignDeliveryRunStatusSending
	run.StartedAt = &now

	if err := tx.StartRun(ctx, *run, now); err != nil {
		return err
	}

	if err := tx.Enqueue(ctx, eventpkg.QueueEmailCampaign, run.ID, newEmailDeliveryBulkJob(run.ID)); err != nil {
		return fmt.Errorf("enqueue email campaign %s: %w", run.ID, err)
	}
	return nil
}

// DueEmailDeliveryRuns lists up to limit scheduled runs whose time has come,
// earliest first.
func (d *CampaignDeliveryDispatcher) DueEmailDeliveryRuns(ctx context.Context, joined Joined, limit int) ([]DeliveryRun, error) {
	if limit <= 0 {
		limit = 25
	}
	return joined.Store.DueRuns(ctx, d.now(), limit)
}

// ActiveEmailDeliveryRuns lists up to limit sending runs that have not
// completed, least recently updated first.
func (*CampaignDeliveryDispatcher) ActiveEmailDeliveryRuns(ctx context.Context, joined Joined, limit int) ([]DeliveryRun, error) {
	if limit <= 0 {
		limit = 25
	}
	return joined.Store.ActiveRuns(ctx, limit)
}

// ResumableEmailDeliveryRun reports whether the active run's frozen target
// still reconstructs, and logs why when it does not.
func (*CampaignDeliveryDispatcher) ResumableEmailDeliveryRun(ctx context.Context, joined Joined, run DeliveryRun) bool {
	target, err := loadCampaignDeliveryTarget(ctx, joined.Store, run)
	if err != nil {
		slog.ErrorContext(ctx, "failed to load campaign delivery target", "run_id", run.ID, failurecause.Attr(err))
		return false
	}
	if err := ValidateCampaignDeliveryTarget(target); err != nil {
		slog.ErrorContext(ctx, "failed to reconstruct campaign delivery target", "run_id", run.ID, failurecause.Attr(err))
		return false
	}
	return true
}

// MaterializeEmailDeliveryRun materializes the run's recipients in tx.
func (d *CampaignDeliveryDispatcher) MaterializeEmailDeliveryRun(ctx context.Context, joined Joined, runID string) error {
	return MaterializeCampaignDeliveryRun(ctx, joined, runID)
}

// PublishResumedEmailDeliveryRun hands a materialized run that still has
// pending recipients back to bulk delivery. It reports whether it did, and
// logs a failure.
func (d *CampaignDeliveryDispatcher) PublishResumedEmailDeliveryRun(ctx context.Context, joined Joined, runID string) bool {
	pending, err := HasPendingCampaignDeliveryRecipients(ctx, joined, runID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to inspect pending campaign recipients", "run_id", runID, failurecause.Attr(err))
		return false
	}
	if !pending {
		return false
	}
	if err := d.publisher.PublishSendBulkEmail(ctx, newEmailDeliveryBulkJob(runID)); err != nil {
		slog.ErrorContext(ctx, "failed to publish campaign delivery resume job", "run_id", runID, failurecause.Attr(err))
		return false
	}
	return true
}

// DispatchEmailDeliveryRun moves the due run to sending in tx and enqueues
// its first batch beside it; skipLocked passes over a run another
// dispatcher holds. It reports whether the run was handed to delivery,
// which holds once tx commits.
func (d *CampaignDeliveryDispatcher) DispatchEmailDeliveryRun(ctx context.Context, joined Joined, runID string, skipLocked bool) (bool, error) {
	tx := joined.Store

	locked, skipped, err := tx.LockDeliveryRun(ctx, runID, skipLocked)
	if err != nil || skipped {
		return false, err
	}
	run := locked.Run
	if run.Status != CampaignDeliveryRunStatusScheduled {
		return false, nil
	}
	now := d.now().UTC()
	if run.ScheduledAt.After(now) {
		return false, nil
	}
	target, err := loadCampaignDeliveryTarget(ctx, tx, run)
	if err != nil {
		return false, err
	}

	count, err := tx.CountRecipients(ctx, target)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(run.RunKind) {
	case EmailDeliveryRunKindCampaign:
		err = transitionCampaignDeliveryRunToDispatch(ctx, joined, locked.Campaign, &run, count, now)
	case EmailDeliveryRunKindLegalNotice:
		err = transitionLegalNoticeDeliveryRunToDispatch(ctx, tx, &run, count, now)
	default:
		err = fmt.Errorf("unsupported email delivery run kind: %s", run.RunKind)
	}
	if err != nil || run.Status != CampaignDeliveryRunStatusSending {
		return false, err
	}
	if err := tx.Enqueue(ctx, eventpkg.QueueEmailCampaign, run.ID, newEmailDeliveryBulkJob(run.ID)); err != nil {
		return false, fmt.Errorf("enqueue email campaign %s: %w", run.ID, err)
	}
	return true, nil
}

func transitionCampaignDeliveryRunToDispatch(
	ctx context.Context,
	joined Joined,
	lockedCampaign *Campaign,
	run *DeliveryRun,
	count int64,
	now time.Time,
) error {
	tx := joined.Store
	campaignID := strings.TrimSpace(ptrStringValue(run.CampaignID))
	if campaignID == "" {
		return fmt.Errorf("campaign delivery run missing campaign id")
	}
	if count == 0 {

		if err := tx.FailRun(ctx, run.ID, now); err != nil {
			return err
		}

		if lockedCampaign == nil || lockedCampaign.ID != campaignID {
			return fmt.Errorf("campaign delivery run campaign lock is missing")
		}
		previousStatus := lockedCampaign.Status
		if err := tx.UpdateCampaignStatus(ctx, *lockedCampaign, CampaignStatusFailed); err != nil {
			return err
		}
		lockedCampaign.Status = CampaignStatusFailed
		run.Status = CampaignDeliveryRunStatusFailed
		run.CompletedAt = &now
		return appendCampaignTerminalStatusAudit(ctx, joined, campaignID, campaignAuditState(previousStatus), sharedtelemetry.AuditStateFailed)
	}
	run.Status = CampaignDeliveryRunStatusSending
	run.StartedAt = &now
	run.TargetCount = int(count)

	if err := tx.DispatchRun(ctx, *run, now); err != nil {
		return err
	}
	changed, err := tx.DispatchCampaign(ctx, campaignID, run.TargetRecipientScope)
	if err != nil {
		return err
	}

	if !changed {
		return fmt.Errorf("campaign delivery lifecycle is no longer dispatchable")
	}

	return nil
}

func transitionLegalNoticeDeliveryRunToDispatch(
	ctx context.Context,
	tx Store,
	run *DeliveryRun,
	count int64,
	now time.Time,
) error {
	run.TargetCount = int(count)
	if count == 0 {
		run.Status = CampaignDeliveryRunStatusSkipped
		run.CompletedAt = &now

		return tx.DispatchRun(ctx, *run, now)
	}
	if err := tx.PrepareLegalNoticePreview(ctx, *run, now); err != nil {
		return err
	}

	run.Status = CampaignDeliveryRunStatusSending
	run.StartedAt = &now
	return tx.DispatchRun(ctx, *run, now)
}

func newEmailDeliveryBulkJob(runID string) *managev1.SendBulkEmailBatchEvent {
	return &managev1.SendBulkEmailBatchEvent{
		DeliveryRunId: strings.TrimSpace(runID),
		BatchSize:     100,
		RatePerSecond: 10,
	}
}
