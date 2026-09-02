package campaign

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
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

type campaignDeliveryCompletionCounts struct {
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
	CampaignStatus string
}

func decideEmailDeliveryCompletion(counts campaignDeliveryCompletionCounts, targetCount int, runKind string) campaignDeliveryCompletionDecision {
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

func campaignStatusForCompletedRun(runStatus string, runKind string) string {
	if runKind != EmailDeliveryRunKindCampaign {
		return ""
	}
	switch runStatus {
	case CampaignDeliveryRunStatusSent:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String()
	case CampaignDeliveryRunStatusFailed:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String()
	default:
		return ""
	}
}

type CampaignBulkPublisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
	PublishSendBulkEmail(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error
}

type CampaignDeliveryDispatcher struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	publisher   CampaignBulkPublisher
	auditWriter domainaudit.Appender
	legalNotice LegalNoticeDeliveryPort
}

type CampaignDeliveryDispatcherOption func(*CampaignDeliveryDispatcher)

func WithLegalNoticeDeliveryPort(port LegalNoticeDeliveryPort) CampaignDeliveryDispatcherOption {
	return func(dispatcher *CampaignDeliveryDispatcher) {
		dispatcher.legalNotice = port
	}
}

// NewAuditedCampaignDeliveryDispatcher is used by API and worker production
// paths.
func NewAuditedCampaignDeliveryDispatcher(db *gorm.DB, spiceDB *auth.SpiceDBClient, publisher CampaignBulkPublisher, auditWriter domainaudit.Appender, options ...CampaignDeliveryDispatcherOption) *CampaignDeliveryDispatcher {
	if auditWriter == nil {
		panic("CampaignDeliveryDispatcher: audit writer is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dispatcher := &CampaignDeliveryDispatcher{db: db, spiceDB: spiceDB, publisher: publisher, auditWriter: auditWriter}
	for _, option := range options {
		option(dispatcher)
	}
	return dispatcher
}

func (d *CampaignDeliveryDispatcher) DispatchEmailDeliveryRun(ctx context.Context, runID string) error {
	_, err := d.dispatchRun(ctx, runID, "")
	return err
}

// enqueueStartedCampaignDeliveryRun records the first email.campaign command
// beside an immediate-send run. The run has already captured its immutable
// definition; the worker performs recipient materialization after this durable
// handoff commits.
func enqueueStartedCampaignDeliveryRun(
	ctx context.Context,
	tx *gorm.DB,
	publisher CampaignBulkPublisher,
	run *model.CampaignDeliveryRun,
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
	if err := tx.Model(&model.CampaignDeliveryRun{}).Where("id = ? AND status = ?", run.ID, CampaignDeliveryRunStatusScheduled).Updates(structured.Fields{
		"status": run.Status, "started_at": now, "target_count": run.TargetCount,
	}).Error; err != nil {
		return err
	}
	if err := publishCampaignDurableProtoInTransaction(ctx, publisher, tx, eventpkg.QueueEmailCampaign, run.ID, newEmailDeliveryBulkJob(run.ID)); err != nil {
		return fmt.Errorf("enqueue email campaign %s: %w", run.ID, err)
	}
	return nil
}

func (d *CampaignDeliveryDispatcher) DispatchDueEmailDeliveryRuns(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 25
	}

	var runs []model.CampaignDeliveryRun
	now := time.Now()
	if err := d.db.WithContext(ctx).
		Where("status = ? AND scheduled_at <= ?", CampaignDeliveryRunStatusScheduled, now).
		Order("scheduled_at ASC, id ASC").
		Limit(limit).
		Find(&runs).Error; err != nil {
		return 0, errs.Internal(err)
	}

	dispatched := 0
	for _, run := range runs {
		published, err := d.dispatchRun(ctx, run.ID, "SKIP LOCKED")
		if err != nil {
			slog.Error("failed to dispatch email delivery run", "run_id", run.ID, "run_kind", run.RunKind, "error", err)
			continue
		}
		if published {
			dispatched++
		}
	}

	return dispatched, nil
}

func (d *CampaignDeliveryDispatcher) ResumeActiveEmailDeliveryRuns(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 25
	}

	var runs []model.CampaignDeliveryRun
	if err := d.db.WithContext(ctx).
		Where("status = ?", CampaignDeliveryRunStatusSending).
		Where("completed_at IS NULL").
		Order("updated_at ASC, id ASC").
		Limit(limit).
		Find(&runs).Error; err != nil {
		return 0, errs.Internal(err)
	}

	resumed := 0
	for _, run := range runs {
		target, err := loadCampaignDeliveryTarget(ctx, d.db, run)
		if err != nil {
			slog.Error("failed to load campaign delivery target", "run_id", run.ID, "error", err)
			continue
		}
		if _, err := campaignDeliveryTargetRecipientSelection(target); err != nil {
			slog.Error("failed to reconstruct campaign delivery target", "run_id", run.ID, "error", err)
			continue
		}
		job := newEmailDeliveryBulkJob(run.ID)
		if err := MaterializeCampaignDeliveryRun(ctx, d.db, d.spiceDB, run.ID); err != nil {
			slog.Error(
				"failed to recover campaign delivery materialization",
				"run_id",
				run.ID,
				"error",
				err,
			)
			continue
		}

		pending, err := HasPendingCampaignDeliveryRecipients(ctx, d.db, run.ID)
		if err != nil {
			slog.Error("failed to inspect pending campaign recipients", "run_id", run.ID, "error", err)
			continue
		}
		if !pending {
			continue
		}
		if err := d.publisher.PublishSendBulkEmail(ctx, job); err != nil {
			slog.Error("failed to publish campaign delivery resume job", "run_id", run.ID, "error", err)
			continue
		}
		resumed++
	}
	return resumed, nil
}

func (d *CampaignDeliveryDispatcher) dispatchRun(ctx context.Context, runID string, lockOptions string) (bool, error) {
	_, published, err := d.prepareDeliveryRunDispatch(ctx, runID, lockOptions)
	return published, err
}

func (d *CampaignDeliveryDispatcher) prepareDeliveryRunDispatch(
	ctx context.Context,
	runID string,
	lockOptions string,
) (model.CampaignDeliveryRun, bool, error) {
	var run model.CampaignDeliveryRun
	skippedLockedRow := false
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, skipped, err := lockEmailDeliveryRunForMutation(
			ctx,
			tx,
			runID,
			lockOptions,
		)
		if err != nil {
			return err
		}
		if skipped {
			skippedLockedRow = true
			return nil
		}
		run = locked.Run
		if run.Status != CampaignDeliveryRunStatusScheduled {
			return nil
		}
		now := time.Now().UTC()
		if run.ScheduledAt.After(now) {
			return nil
		}

		target, err := loadCampaignDeliveryTarget(ctx, tx, run)
		if err != nil {
			return err
		}
		selection, err := campaignDeliveryTargetRecipientSelection(target)
		if err != nil {
			return err
		}
		count, err := countBulkEmailRecipients(ctx, tx, d.spiceDB, selection)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(run.RunKind) {
		case EmailDeliveryRunKindCampaign:
			if err := transitionCampaignDeliveryRunToDispatch(ctx, tx, d.auditWriter, locked.Campaign, &run, count, now); err != nil {
				return err
			}
		case EmailDeliveryRunKindLegalNotice:
			if err := transitionLegalNoticeDeliveryRunToDispatch(ctx, tx, d.legalNotice, &run, count, now); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported email delivery run kind: %s", run.RunKind)
		}
		if run.Status != CampaignDeliveryRunStatusSending {
			return nil
		}
		if err := publishCampaignDurableProtoInTransaction(ctx, d.publisher, tx, eventpkg.QueueEmailCampaign, run.ID, newEmailDeliveryBulkJob(run.ID)); err != nil {
			return fmt.Errorf("enqueue email campaign %s: %w", run.ID, err)
		}
		return nil
	})
	if err != nil {
		return model.CampaignDeliveryRun{}, false, err
	}
	if skippedLockedRow || run.Status != CampaignDeliveryRunStatusSending {
		return run, false, nil
	}
	return run, true, nil
}

func transitionCampaignDeliveryRunToDispatch(
	ctx context.Context,
	tx *gorm.DB,
	auditWriter domainaudit.Appender,
	lockedCampaign *model.Campaign,
	run *model.CampaignDeliveryRun,
	count int64,
	now time.Time,
) error {
	campaignID := strings.TrimSpace(ptrStringValue(run.CampaignID))
	if campaignID == "" {
		return fmt.Errorf("campaign delivery run missing campaign id")
	}
	if count == 0 {
		if err := tx.Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(structured.Fields{
			"status": CampaignDeliveryRunStatusFailed, "last_error": "campaign has no recipients", "completed_at": now,
		}).Error; err != nil {
			return err
		}
		if lockedCampaign == nil || lockedCampaign.ID != campaignID {
			return fmt.Errorf("campaign delivery run campaign lock is missing")
		}
		previousStatus := lockedCampaign.Status
		if err := tx.Model(lockedCampaign).Update("status", managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String()).Error; err != nil {
			return err
		}
		lockedCampaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED.String()
		run.Status = CampaignDeliveryRunStatusFailed
		run.CompletedAt = &now
		return appendCampaignTerminalStatusAudit(ctx, tx, auditWriter, campaignID, campaignAuditState(previousStatus), sharedtelemetry.AuditStateFailed)
	}
	run.Status = CampaignDeliveryRunStatusSending
	run.StartedAt = &now
	run.TargetCount = int(count)
	if err := tx.Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(structured.Fields{
		"status": run.Status, "started_at": now, "target_count": run.TargetCount,
	}).Error; err != nil {
		return err
	}
	result := tx.Model(&model.Campaign{}).
		Where("id = ? AND status IN ?", campaignID, []string{
			managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
			managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
		}).
		Updates(structured.Fields{
			"status": managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(), "scheduled_at": nil,
			"sent_at": nil, "sent_count": 0, "recipient_scope": run.TargetRecipientScope,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("campaign delivery lifecycle is no longer dispatchable")
	}
	return nil
}

func transitionLegalNoticeDeliveryRunToDispatch(
	ctx context.Context,
	tx *gorm.DB,
	legalNotice LegalNoticeDeliveryPort,
	run *model.CampaignDeliveryRun,
	count int64,
	now time.Time,
) error {
	run.TargetCount = int(count)
	if count == 0 {
		run.Status = CampaignDeliveryRunStatusSkipped
		run.CompletedAt = &now
		return tx.Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(structured.Fields{
			"status": run.Status, "target_count": run.TargetCount, "completed_at": now,
		}).Error
	}
	if legalNotice == nil {
		return errs.DependencyUnavailable("Legal notice delivery")
	}
	if err := legalNotice.PrepareAutomaticPreviewShareLink(ctx, tx, *run, now); err != nil {
		return err
	}
	run.Status = CampaignDeliveryRunStatusSending
	run.StartedAt = &now
	return tx.Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(structured.Fields{
		"status": run.Status, "started_at": now, "target_count": run.TargetCount,
	}).Error
}

type lockedEmailDeliveryRun struct {
	Run      model.CampaignDeliveryRun
	Campaign *model.Campaign
}

// lockEmailDeliveryRunForMutation enforces the campaign -> run lock order for
// every transaction that mutates both rows. The preliminary run read only
// resolves the immutable campaign relationship; it does not acquire a row
// lock.
func lockEmailDeliveryRunForMutation(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	lockOptions string,
) (lockedEmailDeliveryRun, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return lockedEmailDeliveryRun{}, false, gorm.ErrRecordNotFound
	}

	var identity struct {
		RunKind    string  `gorm:"column:run_kind"`
		CampaignID *string `gorm:"column:campaign_id"`
	}
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Select("run_kind", "campaign_id").
		First(&identity, "id = ?", runID).Error; err != nil {
		return lockedEmailDeliveryRun{}, false, err
	}

	lock := clause.Locking{Strength: "UPDATE", Options: lockOptions}
	locked := lockedEmailDeliveryRun{}
	campaignID := strings.TrimSpace(ptrStringValue(identity.CampaignID))
	campaign, skipped, err := lockEmailDeliveryCampaign(
		ctx,
		tx,
		lock,
		identity.RunKind,
		campaignID,
		lockOptions,
	)
	if err != nil || skipped {
		return lockedEmailDeliveryRun{}, skipped, err
	}
	locked.Campaign = campaign

	if err := tx.WithContext(ctx).
		Clauses(lock).
		First(&locked.Run, "id = ?", runID).Error; err != nil {
		if lockOptions != "" && err == gorm.ErrRecordNotFound {
			return lockedEmailDeliveryRun{}, true, nil
		}
		return lockedEmailDeliveryRun{}, false, err
	}
	if locked.Run.RunKind != identity.RunKind ||
		strings.TrimSpace(ptrStringValue(locked.Run.CampaignID)) != campaignID {
		return lockedEmailDeliveryRun{}, false, fmt.Errorf(
			"email delivery run relationship changed while acquiring locks",
		)
	}
	return locked, false, nil
}

func lockEmailDeliveryCampaign(
	ctx context.Context,
	tx *gorm.DB,
	lock clause.Locking,
	runKind string,
	campaignID string,
	lockOptions string,
) (*model.Campaign, bool, error) {
	if runKind != EmailDeliveryRunKindCampaign {
		return nil, false, nil
	}
	if campaignID == "" {
		return nil, false, fmt.Errorf("campaign delivery run missing campaign id")
	}

	var campaign model.Campaign
	err := tx.WithContext(ctx).Clauses(lock).First(&campaign, "id = ?", campaignID).Error
	if err == nil {
		return &campaign, false, nil
	}
	if lockOptions != "" && err == gorm.ErrRecordNotFound {
		return nil, true, nil
	}
	return nil, false, err
}

func newEmailDeliveryBulkJob(runID string) *managev1.SendBulkEmailBatchEvent {
	return &managev1.SendBulkEmailBatchEvent{
		DeliveryRunId: strings.TrimSpace(runID),
		BatchSize:     100,
		RatePerSecond: 10,
	}
}
