package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type CampaignDeliveryRecipientJob struct {
	Recipient model.CampaignDeliveryRecipient
	Run       model.CampaignDeliveryRun
}

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

func MaterializeCampaignDeliveryRun(
	ctx context.Context,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	runID string,
) error {
	if strings.TrimSpace(runID) == "" {
		return errs.Required("run_id")
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, _, err := lockEmailDeliveryRunForMutation(ctx, tx, runID, "")
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
		frozenSelection, err := campaignDeliveryTargetRecipientSelection(target)
		if err != nil {
			return err
		}

		if err := materializeCampaignDeliveryRecipients(ctx, tx, spiceDB, run, frozenSelection); err != nil {
			return err
		}

		var total int64
		if err := tx.Model(&model.CampaignDeliveryRecipient{}).
			Where("run_id = ?", runID).
			Count(&total).Error; err != nil {
			return err
		}
		return tx.Model(&model.CampaignDeliveryRun{}).
			Where("id = ?", runID).
			Update("target_count", int(total)).Error
	})
}

func ListPendingCampaignDeliveryRecipients(
	ctx context.Context,
	db *gorm.DB,
	runID string,
	afterID string,
	limit int,
) ([]CampaignDeliveryRecipientJob, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errs.Required("run_id")
	}
	if limit <= 0 {
		limit = 100
	}

	var run model.CampaignDeliveryRun
	if err := db.WithContext(ctx).First(&run, "id = ?", runID).Error; err != nil {
		return nil, err
	}
	query := db.WithContext(ctx).
		Where("run_id = ? AND status = ?", runID, CampaignDeliveryRecipientStatusPending).
		Order("id ASC").
		Limit(limit)
	if afterID = strings.TrimSpace(afterID); afterID != "" {
		query = query.Where("id > ?", afterID)
	}
	var recipients []model.CampaignDeliveryRecipient
	if err := query.Find(&recipients).Error; err != nil {
		return nil, err
	}
	jobs := make([]CampaignDeliveryRecipientJob, 0, len(recipients))
	for _, recipient := range recipients {
		jobs = append(jobs, CampaignDeliveryRecipientJob{Recipient: recipient, Run: run})
	}
	return jobs, nil
}

func HasPendingCampaignDeliveryRecipients(ctx context.Context, db *gorm.DB, runID string) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Where("run_id = ? AND status = ?", strings.TrimSpace(runID), CampaignDeliveryRecipientStatusPending).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func materializeCampaignDeliveryRecipients(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	run model.CampaignDeliveryRun,
	selection *bulkEmailRecipientSelection,
) error {
	if err := resolveBulkEmailRecipientSelectionPermissions(ctx, spiceDB, selection); err != nil {
		return err
	}
	candidates, err := buildBulkEmailRecipientCandidates(selection)
	if err != nil {
		return err
	}
	return execCampaignRecipientMaterialization(ctx, tx, run, candidates.SQL, candidates.Args...)
}

func execCampaignRecipientMaterialization(
	ctx context.Context,
	tx *gorm.DB,
	run model.CampaignDeliveryRun,
	candidatesSQL string,
	args ...structured.Value) error {
	runID := strings.TrimSpace(run.ID)
	if runID == "" {
		return fmt.Errorf("email delivery run id is required")
	}

	sql := fmt.Sprintf(`
		WITH candidates AS (
			%s
		),
		ranked AS (
			SELECT
				*,
				ROW_NUMBER() OVER (
					PARTITION BY LOWER(TRIM(email))
					ORDER BY priority ASC, sort_at ASC, LOWER(TRIM(email)) ASC
				) AS rn
			FROM candidates
			WHERE email IS NOT NULL
				AND TRIM(email) <> ''
		)
		INSERT INTO email_delivery_recipient (
			run_id,
			recipient_email,
			normalized_recipient_email,
			member_id,
			identity_id,
			locale,
			recipient_context_type,
			status
		)
		SELECT
			?,
			TRIM(email),
			LOWER(TRIM(email)),
			member_id,
			identity_id,
			NULLIF(locale, ''),
			context_kind,
			?
		FROM ranked
		WHERE rn = 1
		ON CONFLICT (run_id, normalized_recipient_email) DO NOTHING
	`, candidatesSQL)

	execArgs := make(structured.Values, 0, len(args)+2)
	execArgs = append(execArgs, args...)
	execArgs = append(execArgs, runID, CampaignDeliveryRecipientStatusPending)
	return tx.WithContext(ctx).Exec(sql, execArgs...).Error
}

func CampaignDeliveryRecipientNeedsDelivery(
	ctx context.Context,
	db *gorm.DB,
	recipientID string,
) (bool, error) {
	recipientID = strings.TrimSpace(recipientID)
	if recipientID == "" {
		return true, nil
	}
	var recipient model.CampaignDeliveryRecipient
	if err := db.WithContext(ctx).Select("status").First(&recipient, "id = ?", recipientID).Error; err != nil {
		return false, err
	}
	if recipient.Status == CampaignDeliveryRecipientStatusPending {
		return true, nil
	}
	if campaignDeliveryRecipientTerminalStatus(recipient.Status) {
		return false, nil
	}
	return false, fmt.Errorf("campaign delivery recipient has unsupported status %q", recipient.Status)
}

// MarkCampaignDeliveryRecipientResultWithAudit finalizes the authoritative
// recipient result. Only an actual Campaign terminal transition produces the
// backend Audit record; per-recipient outcomes deliberately never do.
func MarkCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	recipientID string,
	status string,
	providerMessageID string,
	errorType string,
	metrics CampaignDeliveryMetrics,
) error {
	recipientID = strings.TrimSpace(recipientID)
	if recipientID == "" {
		return nil
	}
	providerMessageID = strings.TrimSpace(providerMessageID)
	errorType = strings.TrimSpace(errorType)
	if !campaignDeliveryRecipientStatusFinalizesRun(status) {
		return fmt.Errorf("campaign delivery result status %q is not terminal", status)
	}
	if (status == CampaignDeliveryRecipientStatusSent || status == CampaignDeliveryRecipientStatusDelivered) && providerMessageID == "" {
		return fmt.Errorf("accepted campaign delivery requires provider message id")
	}
	if status != CampaignDeliveryRecipientStatusSent && status != CampaignDeliveryRecipientStatusDelivered && providerMessageID != "" {
		return fmt.Errorf("failed campaign delivery cannot carry provider message id")
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity model.CampaignDeliveryRecipient
		if err := tx.Select("id", "run_id").First(&identity, "id = ?", recipientID).Error; err != nil {
			return err
		}
		locked, _, err := lockEmailDeliveryRunForMutation(ctx, tx, identity.RunID, "")
		if err != nil {
			return err
		}
		var recipient model.CampaignDeliveryRecipient
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "run_id", "status", "provider_message_id", "error_type", "terminal_at").
			First(&recipient, "id = ?", recipientID).Error; err != nil {
			return err
		}
		if recipient.RunID != locked.Run.ID {
			return fmt.Errorf("campaign delivery recipient relationship changed while acquiring locks")
		}
		if campaignDeliveryRecipientTerminalStatus(recipient.Status) {
			return nil
		}
		if recipient.Status != CampaignDeliveryRecipientStatusPending {
			return fmt.Errorf("campaign delivery recipient is not pending")
		}
		now := time.Now()
		updates := structured.Fields{
			"status":              status,
			"provider_message_id": nullableTrimmedString(providerMessageID),
			"error_type":          nullableTrimmedString(errorType),
			"terminal_at":         now,
		}
		result := tx.Model(&model.CampaignDeliveryRecipient{}).
			Where("id = ? AND run_id = ? AND status = ?", recipient.ID, locked.Run.ID, CampaignDeliveryRecipientStatusPending).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return finalizeLockedEmailDeliveryRun(
			ctx,
			tx,
			&locked,
			auditWriter,
			metrics,
		)
	})
}

func finalizeLockedEmailDeliveryRun(
	ctx context.Context,
	tx *gorm.DB,
	locked *lockedEmailDeliveryRun,
	auditWriter domainaudit.Appender,
	metrics CampaignDeliveryMetrics,
) error {
	run := &locked.Run
	if run.Status != CampaignDeliveryRunStatusSending {
		return nil
	}

	var counts campaignDeliveryCompletionCounts
	if err := tx.Model(&model.CampaignDeliveryRecipient{}).
		Select(`
				COUNT(*) AS total,
				COUNT(*) FILTER (WHERE status = ?) AS pending,
				COUNT(*) FILTER (WHERE status = ?) AS sent,
				COUNT(*) FILTER (WHERE status = ?) AS delivered,
				COUNT(*) FILTER (WHERE status = ?) AS skipped,
				COUNT(*) FILTER (WHERE status = ?) AS permanent_fail,
				COUNT(*) FILTER (WHERE status = ?) AS blocked,
				COUNT(*) FILTER (WHERE status = ?) AS suppressed,
				COUNT(*) FILTER (WHERE status = ?) AS bounced,
				COUNT(*) FILTER (WHERE status = ?) AS complained
			`,
			CampaignDeliveryRecipientStatusPending,
			CampaignDeliveryRecipientStatusSent,
			CampaignDeliveryRecipientStatusDelivered,
			CampaignDeliveryRecipientStatusSkipped,
			CampaignDeliveryRecipientStatusPermanentFailed,
			CampaignDeliveryRecipientStatusBlocked,
			CampaignDeliveryRecipientStatusSuppressed,
			CampaignDeliveryRecipientStatusBounced,
			CampaignDeliveryRecipientStatusComplained,
		).
		Where("run_id = ?", run.ID).
		Scan(&counts).Error; err != nil {
		return err
	}

	run.SentCount = int(counts.Sent + counts.Delivered)
	run.SkippedCount = int(counts.Skipped)
	run.FailedCount = int(counts.PermanentFail + counts.Bounced + counts.Complained)
	run.BlockedCount = int(counts.Blocked)
	run.SuppressedCount = int(counts.Suppressed)

	decision := decideEmailDeliveryCompletion(counts, run.TargetCount, run.RunKind)
	if !decision.Complete {
		return tx.Save(run).Error
	}

	now := time.Now()
	run.CompletedAt = &now
	run.Status = decision.RunStatus
	if metrics != nil {
		metrics.RecordRunDuration(ctx, *run, now)
	}
	if err := tx.Save(run).Error; err != nil {
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
	if err := tx.Save(locked.Campaign).Error; err != nil {
		return err
	}
	return appendCampaignTerminalStatusAudit(ctx, tx, auditWriter, campaignID, campaignAuditState(previousStatus), campaignAuditState(decision.CampaignStatus))
}
