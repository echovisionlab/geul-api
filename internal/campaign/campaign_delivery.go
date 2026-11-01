package campaign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
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
		return string(model.CampaignStatusSent)
	case CampaignDeliveryRunStatusFailed:
		return string(model.CampaignStatusFailed)
	default:
		return ""
	}
}

type CampaignBulkPublisher interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
	PublishSendBulkEmail(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error
}

// CampaignDeliveryDispatcher moves due delivery runs to sending and resumes
// active ones. The delivery worker has no request principal to run
// mutation.Run for, so the dispatcher opens no transaction: its caller
// (internal/adapters/campaign) passes the database for reads and one
// transaction per run for each write.
type CampaignDeliveryDispatcher struct {
	spiceDB     *auth.SpiceDBClient
	publisher   CampaignBulkPublisher
	auditWriter domainaudit.Appender
	legalNotice LegalNoticeDeliveryPort
	// now is the dispatcher's clock: which runs are due and when they start.
	now func() time.Time
}

type CampaignDeliveryDispatcherOption func(*CampaignDeliveryDispatcher)

func WithLegalNoticeDeliveryPort(port LegalNoticeDeliveryPort) CampaignDeliveryDispatcherOption {
	return func(dispatcher *CampaignDeliveryDispatcher) {
		dispatcher.legalNotice = port
	}
}

// NewAuditedCampaignDeliveryDispatcher is used by API and worker production
// paths.
func NewAuditedCampaignDeliveryDispatcher(spiceDB *auth.SpiceDBClient, publisher CampaignBulkPublisher, auditWriter domainaudit.Appender, options ...CampaignDeliveryDispatcherOption) *CampaignDeliveryDispatcher {
	if auditWriter == nil {
		panic("CampaignDeliveryDispatcher: audit writer is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dispatcher := &CampaignDeliveryDispatcher{spiceDB: spiceDB, publisher: publisher, auditWriter: auditWriter, now: time.Now}
	for _, option := range options {
		option(dispatcher)
	}
	return dispatcher
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

// DueEmailDeliveryRuns lists up to limit scheduled runs whose time has come,
// earliest first.
func (d *CampaignDeliveryDispatcher) DueEmailDeliveryRuns(ctx context.Context, db *gorm.DB, limit int) ([]model.CampaignDeliveryRun, error) {
	if limit <= 0 {
		limit = 25
	}
	var runs []model.CampaignDeliveryRun
	err := db.WithContext(ctx).
		Where("status = ? AND scheduled_at <= ?", CampaignDeliveryRunStatusScheduled, d.now()).
		Order("scheduled_at ASC, id ASC").
		Limit(limit).
		Find(&runs).Error
	return runs, err
}

// ActiveEmailDeliveryRuns lists up to limit sending runs that have not
// completed, least recently updated first.
func (*CampaignDeliveryDispatcher) ActiveEmailDeliveryRuns(ctx context.Context, db *gorm.DB, limit int) ([]model.CampaignDeliveryRun, error) {
	if limit <= 0 {
		limit = 25
	}
	var runs []model.CampaignDeliveryRun
	err := db.WithContext(ctx).
		Where("status = ?", CampaignDeliveryRunStatusSending).
		Where("completed_at IS NULL").
		Order("updated_at ASC, id ASC").
		Limit(limit).
		Find(&runs).Error
	return runs, err
}

// ResumableEmailDeliveryRun reports whether the active run's frozen target
// still reconstructs, and logs why when it does not.
func (*CampaignDeliveryDispatcher) ResumableEmailDeliveryRun(ctx context.Context, db *gorm.DB, run model.CampaignDeliveryRun) bool {
	target, err := loadCampaignDeliveryTarget(ctx, db, run)
	if err != nil {
		slog.ErrorContext(ctx, "failed to load campaign delivery target", "run_id", run.ID, failurecause.Attr(err))
		return false
	}
	if _, err := campaignDeliveryTargetRecipientSelection(target); err != nil {
		slog.ErrorContext(ctx, "failed to reconstruct campaign delivery target", "run_id", run.ID, failurecause.Attr(err))
		return false
	}
	return true
}

// MaterializeEmailDeliveryRun materializes the run's recipients in tx.
func (d *CampaignDeliveryDispatcher) MaterializeEmailDeliveryRun(ctx context.Context, tx *gorm.DB, runID string) error {
	return MaterializeCampaignDeliveryRun(ctx, tx, d.spiceDB, runID)
}

// PublishResumedEmailDeliveryRun hands a materialized run that still has
// pending recipients back to bulk delivery. It reports whether it did, and
// logs a failure.
func (d *CampaignDeliveryDispatcher) PublishResumedEmailDeliveryRun(ctx context.Context, db *gorm.DB, runID string) bool {
	pending, err := HasPendingCampaignDeliveryRecipients(ctx, db, runID)
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
func (d *CampaignDeliveryDispatcher) DispatchEmailDeliveryRun(ctx context.Context, tx *gorm.DB, runID string, skipLocked bool) (bool, error) {
	tx = tx.WithContext(ctx)
	lockOptions := ""
	if skipLocked {
		lockOptions = clause.LockingOptionsSkipLocked
	}
	locked, skipped, err := lockEmailDeliveryRunForMutation(ctx, tx, runID, lockOptions)
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
	selection, err := campaignDeliveryTargetRecipientSelection(target)
	if err != nil {
		return false, err
	}
	count, err := countBulkEmailRecipients(ctx, tx, d.spiceDB, selection)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(run.RunKind) {
	case EmailDeliveryRunKindCampaign:
		err = transitionCampaignDeliveryRunToDispatch(ctx, tx, d.auditWriter, locked.Campaign, &run, count, now)
	case EmailDeliveryRunKindLegalNotice:
		err = transitionLegalNoticeDeliveryRunToDispatch(ctx, tx, d.legalNotice, &run, count, now)
	default:
		err = fmt.Errorf("unsupported email delivery run kind: %s", run.RunKind)
	}
	if err != nil || run.Status != CampaignDeliveryRunStatusSending {
		return false, err
	}
	if err := publishCampaignDurableProtoInTransaction(ctx, d.publisher, tx, eventpkg.QueueEmailCampaign, run.ID, newEmailDeliveryBulkJob(run.ID)); err != nil {
		return false, fmt.Errorf("enqueue email campaign %s: %w", run.ID, err)
	}
	return true, nil
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
		if err := tx.Model(lockedCampaign).Update("status", string(model.CampaignStatusFailed)).Error; err != nil {
			return err
		}
		lockedCampaign.Status = string(model.CampaignStatusFailed)
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
			string(model.CampaignStatusScheduled),
			string(model.CampaignStatusSending),
		}).
		Updates(structured.Fields{
			"status": string(model.CampaignStatusSending), "scheduled_at": nil,
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
		return domainerr.DependencyUnavailable("Legal notice delivery")
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

	lock := clause.Locking{Strength: clause.LockingStrengthUpdate, Options: lockOptions}
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
		if lockOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
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
	if lockOptions != "" && errors.Is(err, gorm.ErrRecordNotFound) {
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
