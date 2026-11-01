package campaignpostgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	execArgs = append(execArgs, runID, campaigndomain.CampaignDeliveryRecipientStatusPending)
	return tx.WithContext(ctx).Exec(sql, execArgs...).Error
}
func (s *store) MaterializeRecipients(
	ctx context.Context,
	run campaigndomain.DeliveryRun,
	target campaigndomain.CampaignDeliveryTarget,
) error {
	selection, err := campaignDeliveryTargetRecipientSelection(target)
	if err != nil {
		return err
	}
	if err := resolveBulkEmailRecipientSelectionPermissions(ctx, s.ports.SpiceDB, selection); err != nil {
		return err
	}
	candidates, err := buildBulkEmailRecipientCandidates(selection)
	if err != nil {
		return err
	}
	return execCampaignRecipientMaterialization(ctx, s.tx, DeliveryRunToRow(run), candidates.SQL, candidates.Args...)
}
func (s *store) ClaimRecipient(ctx context.Context, recipient campaigndomain.DeliveryRecipient, claimID string, now, expiresAt time.Time) (bool, error) {
	tx := s.tx.WithContext(ctx)
	result := tx.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ? AND run_id = ? AND status = ? AND (delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)",
			recipient.ID, recipient.RunID, campaigndomain.CampaignDeliveryRecipientStatusPending, now).
		Updates(structured.Fields{
			"delivery_claim_id":         claimID,
			"delivery_claim_expires_at": expiresAt,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
func (s *store) ReleaseRecipientClaim(ctx context.Context, recipient campaigndomain.DeliveryRecipient, claimID string) error {
	tx := s.tx.WithContext(ctx)
	return tx.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ? AND run_id = ? AND status = ? AND delivery_claim_id = ?",
			recipient.ID, recipient.RunID, campaigndomain.CampaignDeliveryRecipientStatusPending, claimID).
		Updates(structured.Fields{
			"delivery_claim_id":         nil,
			"delivery_claim_expires_at": nil,
		}).Error

}
func (s *store) StoreRecipientResult(ctx context.Context, r campaigndomain.RecipientResult, runID string, now time.Time) error {
	tx := s.tx.WithContext(ctx)
	query := tx.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ? AND run_id = ? AND status = ?", r.RecipientID, runID, campaigndomain.CampaignDeliveryRecipientStatusPending)
	if r.RequireClaim {
		query = query.Where("delivery_claim_id = ? AND delivery_claim_expires_at > ?", r.ClaimID, now)
	} else {
		query = query.Where("(delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)", now)
	}
	result := query.Updates(structured.Fields{
		"status":                    r.Status,
		"provider_message_id":       nullableTrimmedString(r.ProviderMessageID),
		"error_type":                nullableTrimmedString(r.ErrorType),
		"terminal_at":               now,
		"delivery_claim_id":         nil,
		"delivery_claim_expires_at": nil,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: campaigndomain.ErrNotFound}
	}
	return nil
}
func (s *store) CompletionCounts(ctx context.Context, id string) (campaigndomain.DeliveryCompletionCounts, error) {
	tx := s.tx.WithContext(ctx)
	run := campaigndomain.DeliveryRun{ID: id}
	var counts campaigndomain.DeliveryCompletionCounts
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
			campaigndomain.CampaignDeliveryRecipientStatusPending,
			campaigndomain.CampaignDeliveryRecipientStatusSent,
			campaigndomain.CampaignDeliveryRecipientStatusDelivered,
			campaigndomain.CampaignDeliveryRecipientStatusSkipped,
			campaigndomain.CampaignDeliveryRecipientStatusPermanentFailed,
			campaigndomain.CampaignDeliveryRecipientStatusBlocked,
			campaigndomain.CampaignDeliveryRecipientStatusSuppressed,
			campaigndomain.CampaignDeliveryRecipientStatusBounced,
			campaigndomain.CampaignDeliveryRecipientStatusComplained,
		).
		Where("run_id = ?", run.ID).
		Scan(&counts).Error; err != nil {
		return campaigndomain.DeliveryCompletionCounts{}, err
	}

	return counts, nil
}
func (s *store) ProviderOutcomeRunIDs(ctx context.Context, providerMessageID string) ([]string, error) {
	var runIDs []string
	err := s.tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Distinct("run_id").
		Where("provider_message_id = ?", providerMessageID).
		Order("run_id ASC").
		Pluck("run_id", &runIDs).Error
	return runIDs, err
}
func (s *store) LockProviderRecipients(
	ctx context.Context,
	runID string,
	providerMessageID string,
) ([]campaigndomain.DeliveryRecipient, error) {
	var recipients []model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("run_id = ? AND provider_message_id = ?", runID, providerMessageID).
		Order("id ASC").
		Find(&recipients).Error
	return deliveryRecipients(recipients), err
}
func (s *store) UpdateProviderRecipient(ctx context.Context, recipient campaigndomain.DeliveryRecipient, status string, eventAt time.Time, errorType *string) (int, error) {
	tx := s.tx.WithContext(ctx)
	updates := structured.Fields{
		"status": status, "terminal_at": eventAt, "updated_at": eventAt,
		"error_type": errorType,
	}
	updated := tx.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ? AND status = ?", recipient.ID, recipient.Status).
		Updates(updates)
	return int(updated.RowsAffected), updated.Error
}
func (s *store) RefreshBusinessCounts(ctx context.Context, runID string, counts campaigndomain.DeliveryCompletionCounts) error {
	tx := s.tx.WithContext(ctx)
	return tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", runID).
		Updates(structured.Fields{
			"sent_count":       int(counts.Sent + counts.Delivered),
			"skipped_count":    int(counts.Skipped),
			"failed_count":     int(counts.PermanentFail + counts.Bounced + counts.Complained),
			"blocked_count":    int(counts.Blocked),
			"suppressed_count": int(counts.Suppressed),
		}).Error
}
func (s *store) FindRecipient(ctx context.Context, id string) (campaigndomain.DeliveryRecipient, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error
	return DeliveryRecipientFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) LockClaimRecipient(ctx context.Context, id string) (campaigndomain.DeliveryRecipient, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "run_id", "status", "delivery_claim_id", "delivery_claim_expires_at").First(&row, "id = ?", id).Error
	return DeliveryRecipientFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) LockResultRecipient(ctx context.Context, id string) (campaigndomain.DeliveryRecipient, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "run_id", "status", "provider_message_id", "error_type", "terminal_at", "delivery_claim_id", "delivery_claim_expires_at").First(&row, "id = ?", id).Error
	return DeliveryRecipientFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) RecipientCount(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRecipient{}).Where("run_id = ?", id).Count(&count).Error
	return count, err
}
func (s *store) SetTargetCount(ctx context.Context, id string, count int) error {
	return s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ?", id).Update("target_count", count).Error
}
func (s *store) PendingRecipients(ctx context.Context, id, after string, limit int, now time.Time) ([]campaigndomain.DeliveryRecipient, error) {
	query := s.tx.WithContext(ctx).Where("run_id = ? AND status = ?", id, campaigndomain.CampaignDeliveryRecipientStatusPending).Where("(delivery_claim_id IS NULL OR delivery_claim_expires_at <= ?)", now).Order("id ASC").Limit(limit)
	if after != "" {
		query = query.Where("id > ?", after)
	}
	var rows []model.CampaignDeliveryRecipient
	err := query.Find(&rows).Error
	return deliveryRecipients(rows), err
}
func (s *store) HasPendingRecipients(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.CampaignDeliveryRecipient{}).Where("run_id = ? AND status = ?", id, campaigndomain.CampaignDeliveryRecipientStatusPending).Count(&count).Error
	return count > 0, err
}
func deliveryRecipients(rows []model.CampaignDeliveryRecipient) []campaigndomain.DeliveryRecipient {
	if rows == nil {
		return nil
	}
	result := make([]campaigndomain.DeliveryRecipient, len(rows))
	for i, row := range rows {
		result[i] = DeliveryRecipientFromRow(row)
	}
	return result
}

func (s *store) RecipientIdentity(ctx context.Context, id string) (campaigndomain.DeliveryRecipient, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).Select("id", "run_id").First(&row, "id = ?", id).Error
	return DeliveryRecipientFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) RecipientStatus(ctx context.Context, id string) (string, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).Select("status").First(&row, "id = ?", id).Error
	return row.Status, notFound(err, campaigndomain.ErrNotFound)
}
func (s *store) LockReleaseRecipient(ctx context.Context, id string) (campaigndomain.DeliveryRecipient, error) {
	var row model.CampaignDeliveryRecipient
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "run_id", "status", "delivery_claim_id").First(&row, "id = ?", id).Error
	return DeliveryRecipientFromRow(row), notFound(err, campaigndomain.ErrNotFound)
}
