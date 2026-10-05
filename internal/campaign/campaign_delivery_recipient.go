package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
		Where("(delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)", time.Now().UTC()).
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

// ClaimCampaignDeliveryRecipient atomically reserves a pending recipient for
// one worker. The claim is reclaimable after its lease expires; the token
// fences terminal writes and releases from an earlier owner.
func ClaimCampaignDeliveryRecipient(
	ctx context.Context,
	db *gorm.DB,
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
	claimed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			Select("id", "run_id", "status", "delivery_claim_id", "delivery_claim_expires_at").
			First(&recipient, "id = ?", recipientID).Error; err != nil {
			return err
		}
		if recipient.RunID != locked.Run.ID {
			return fmt.Errorf("campaign delivery recipient relationship changed while acquiring locks")
		}
		// Start the lease only after all ordered locks have been acquired. A
		// worker may otherwise wait behind a slow transaction and receive a
		// claim whose lease is already partly or fully consumed at commit.
		now := time.Now().UTC()
		expiresAt := now.Add(CampaignDeliveryRecipientClaimLease)
		if campaignDeliveryRecipientTerminalStatus(recipient.Status) {
			return nil
		}
		if recipient.Status != CampaignDeliveryRecipientStatusPending {
			return fmt.Errorf("campaign delivery recipient has unsupported status %q", recipient.Status)
		}
		if recipient.DeliveryClaimID != nil && recipient.DeliveryClaimExpiresAt != nil &&
			recipient.DeliveryClaimExpiresAt.After(now) {
			return nil
		}
		result := tx.Model(&model.CampaignDeliveryRecipient{}).
			Where("id = ? AND run_id = ? AND status = ? AND (delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)",
				recipient.ID, locked.Run.ID, CampaignDeliveryRecipientStatusPending, now).
			Updates(structured.Fields{
				"delivery_claim_id":         claimID,
				"delivery_claim_expires_at": expiresAt,
			})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	return claimed, err
}

// ReleaseCampaignDeliveryRecipientClaim releases only the exact claim owner.
// A stale worker cannot clear a newer worker's lease.
func ReleaseCampaignDeliveryRecipientClaim(
	ctx context.Context,
	db *gorm.DB,
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
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity model.CampaignDeliveryRecipient
		if err := tx.Select("id", "run_id").First(&identity, "id = ?", recipientID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		locked, _, err := lockEmailDeliveryRunForMutation(ctx, tx, identity.RunID, "")
		if err != nil {
			return err
		}
		var recipient model.CampaignDeliveryRecipient
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "run_id", "status", "delivery_claim_id").
			First(&recipient, "id = ?", recipientID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
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
		return tx.Model(&model.CampaignDeliveryRecipient{}).
			Where("id = ? AND run_id = ? AND status = ? AND delivery_claim_id = ?",
				recipient.ID, locked.Run.ID, CampaignDeliveryRecipientStatusPending, claimID).
			Updates(structured.Fields{
				"delivery_claim_id":         nil,
				"delivery_claim_expires_at": nil,
			}).Error
	})
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
	return markCampaignDeliveryRecipientResultWithAudit(
		ctx, db, auditWriter, recipientID, "", status, providerMessageID, errorType, metrics, false,
	)
}

// MarkClaimedCampaignDeliveryRecipientResultWithAudit finalizes the
// authoritative recipient result only while the supplied lease token is still
// current and unexpired.
func MarkClaimedCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	db *gorm.DB,
	auditWriter domainaudit.Appender,
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
		ctx, db, auditWriter, recipientID, claimID, status, providerMessageID, errorType, metrics, true,
	)
}

func markCampaignDeliveryRecipientResultWithAudit(
	ctx context.Context,
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	recipientID string,
	claimID string,
	status string,
	providerMessageID string,
	errorType string,
	metrics CampaignDeliveryMetrics,
	requireClaim bool,
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
			Select("id", "run_id", "status", "provider_message_id", "error_type", "terminal_at", "delivery_claim_id", "delivery_claim_expires_at").
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
		now := time.Now().UTC()
		if requireClaim {
			if recipient.DeliveryClaimID == nil || *recipient.DeliveryClaimID != claimID ||
				recipient.DeliveryClaimExpiresAt == nil || !recipient.DeliveryClaimExpiresAt.After(now) {
				return ErrCampaignDeliveryClaimLost
			}
		} else if recipient.DeliveryClaimID != nil && recipient.DeliveryClaimExpiresAt != nil &&
			recipient.DeliveryClaimExpiresAt.After(now) {
			return ErrCampaignDeliveryRecipientClaimed
		}
		updates := structured.Fields{
			"status":                    status,
			"provider_message_id":       nullableTrimmedString(providerMessageID),
			"error_type":                nullableTrimmedString(errorType),
			"terminal_at":               now,
			"delivery_claim_id":         nil,
			"delivery_claim_expires_at": nil,
		}
		query := tx.Model(&model.CampaignDeliveryRecipient{}).
			Where("id = ? AND run_id = ? AND status = ?", recipient.ID, locked.Run.ID, CampaignDeliveryRecipientStatusPending)
		if requireClaim {
			query = query.Where("delivery_claim_id = ? AND delivery_claim_expires_at > ?", claimID, now)
		} else {
			query = query.Where("(delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)", now)
		}
		result := query.Updates(updates)
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
