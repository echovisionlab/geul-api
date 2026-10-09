package legal

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Delivery definitions remain sealed historical snapshots after their policy
// is deleted. Lock runs in the delivery worker's order, stop future dispatch,
// and terminalize pending recipients so queued jobs cannot claim them again.
func cancelLegalNoticeDeliveryForPolicyDeletion(ctx context.Context, tx *gorm.DB, kind, id string) error {
	if _, err := legalDocumentPolicyForType(kind); err != nil {
		return err
	}
	var runs []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(kind+"_id = ?", id).Order("id ASC").Find(&runs).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, run := range runs {
		skipped := tx.WithContext(ctx).Model(&model.CampaignDeliveryRecipient{}).
			Where("run_id = ? AND status = ?", run.ID, "pending").Updates(structured.Fields{
			"status": "skipped", "error_type": "policy_deleted", "terminal_at": now,
			"delivery_claim_id": nil, "delivery_claim_expires_at": nil, "updated_at": now,
		})
		if skipped.Error != nil {
			return skipped.Error
		}
		updates := structured.Fields{}
		if skipped.RowsAffected > 0 {
			updates["skipped_count"] = gorm.Expr("skipped_count + ?", skipped.RowsAffected)
			updates["updated_at"] = now
		}
		if run.Status == CampaignDeliveryRunStatusScheduled || run.Status == CampaignDeliveryRunStatusSending {
			updates["status"] = CampaignDeliveryRunStatusCancelled
			updates["completed_at"] = now
			updates["updated_at"] = now
		}
		if len(updates) > 0 {
			if err := tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// A deleted active/scheduled policy no longer owns the stable public OG route.
// Select another current policy if present, otherwise release the route.
func refreshLegalRouteAfterPolicyDeletion(ctx context.Context, tx *gorm.DB, og OG, kind, id string, before *CurrentRoute) error {
	if before == nil || before.ID != id {
		return nil
	}
	current, err := og.CurrentForRoute(ctx, tx, kind)
	if err != nil {
		return err
	}
	if current == nil {
		return og.CancelAndRelease(ctx, tx, kind, og.RouteID(kind))
	}
	return og.RequestSaved(ctx, tx, kind, current.ID, "", true, kind+"_policy_deleted")
}
