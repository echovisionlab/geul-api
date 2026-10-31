package campaign

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// The run kind and statuses a legal notice run passes through.
const (
	legalNoticeRunKind   = "legal_notice"
	legalNoticeScheduled = "scheduled"
	legalNoticeSending   = "sending"
	legalNoticeSent      = "sent"
	legalNoticeCancelled = "cancelled"
)

// LegalNoticeRuns reads and stops the Campaign delivery runs of legal policy
// notices for the Legal domain.
type LegalNoticeRuns struct{}

// legalNoticeReference names the run columns that reference a policy kind's
// version and the event key of its update notice.
type legalNoticeReference struct {
	column        string
	versionColumn string
	updateEvent   string
}

func legalNoticeReferenceOf(kind string) (legalNoticeReference, bool) {
	switch kind {
	case "terms":
		return legalNoticeReference{column: "terms_id", versionColumn: "source_terms_version", updateEvent: email.EventTermsUpdate.String()}, true
	case "privacy":
		return legalNoticeReference{column: "privacy_id", versionColumn: "source_privacy_version", updateEvent: email.EventPrivacyUpdate.String()}, true
	default:
		return legalNoticeReference{}, false
	}
}

func requireLegalNoticeReference(kind string) (legalNoticeReference, error) {
	reference, ok := legalNoticeReferenceOf(kind)
	if !ok {
		return reference, fmt.Errorf("unsupported legal notice reference type %q", kind)
	}
	return reference, nil
}

// LatestSealedVersion returns the highest policy version of kind that a
// notice run sealed, or zero.
func (LegalNoticeRuns) LatestSealedVersion(ctx context.Context, tx *gorm.DB, kind string) (int, error) {
	reference, err := requireLegalNoticeReference(kind)
	if err != nil {
		return 0, err
	}
	var version int
	err = tx.WithContext(ctx).Raw(
		`SELECT COALESCE(MAX(` + reference.versionColumn + `), 0) FROM email_delivery_run WHERE ` + reference.column + ` IS NOT NULL`,
	).Scan(&version).Error
	return version, err
}

// CancelScheduledRuns cancels the scheduled notice runs of the version. A run
// that already started sending rejects the cancellation.
func (LegalNoticeRuns) CancelScheduledRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) error {
	reference, ok := legalNoticeReferenceOf(kind)
	if !ok {
		return domainerr.New(domainerr.Invalid, "", "unsupported legal notice reference type")
	}
	var activeRuns []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(reference.column+" = ? AND status IN ?", policyID, []string{legalNoticeScheduled, legalNoticeSending, legalNoticeSent}).
		Order("id ASC").
		Find(&activeRuns).Error; err != nil {
		return err
	}
	for i := range activeRuns {
		if activeRuns[i].Status == legalNoticeSending || activeRuns[i].Status == legalNoticeSent {
			return domainerr.Precondition("legal notice delivery has already started")
		}
	}
	return tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where(reference.column+" = ? AND status = ?", policyID, legalNoticeScheduled).
		Updates(structured.Fields{"status": legalNoticeCancelled, "completed_at": time.Now().UTC()}).Error
}

// StopRunsForDeletedPolicy keeps the sealed delivery definitions of a deleted
// version as history. It locks the runs in the delivery worker's order, stops
// future dispatch, and terminalizes pending recipients so queued jobs cannot
// claim them again.
func (LegalNoticeRuns) StopRunsForDeletedPolicy(ctx context.Context, tx *gorm.DB, kind, policyID string) error {
	reference, err := requireLegalNoticeReference(kind)
	if err != nil {
		return err
	}
	var runs []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(reference.column+" = ?", policyID).Order("id ASC").Find(&runs).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, run := range runs {
		if err := stopDeletedPolicyRun(ctx, tx, run, now); err != nil {
			return err
		}
	}
	return nil
}

func stopDeletedPolicyRun(ctx context.Context, tx *gorm.DB, run model.CampaignDeliveryRun, now time.Time) error {
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
	if run.Status == legalNoticeScheduled || run.Status == legalNoticeSending {
		updates["status"] = legalNoticeCancelled
		updates["completed_at"] = now
		updates["updated_at"] = now
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(updates).Error
}

// CancelUpdateNoticeAfterActivation keeps the dispatcher from selecting an
// obsolete pre-effective update once the version is active. The activation
// already committed, so this is best-effort: a failure is logged, and a run
// that is sending or terminal is never overwritten.
func (LegalNoticeRuns) CancelUpdateNoticeAfterActivation(ctx context.Context, db *gorm.DB, kind, policyID string, now time.Time) {
	reference, ok := legalNoticeReferenceOf(kind)
	if !ok {
		slog.WarnContext(ctx,
			"Legal policy activation committed without update notice cleanup",
			"reference_type", kind,
			"reference_id", policyID,
			"failure_cause", "unsupported legal notice reference type",
		)
		return
	}
	result := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("run_kind = ?", legalNoticeRunKind).
		Where(reference.column+" = ?", policyID).
		Where("template_event_key = ?", reference.updateEvent).
		Where("status = ?", legalNoticeScheduled).
		Updates(structured.Fields{"status": legalNoticeCancelled, "completed_at": now.UTC()})
	if result.Error != nil {
		slog.WarnContext(ctx,
			"Legal policy activation committed without update notice cleanup",
			"reference_type", kind,
			"reference_id", policyID,
			"event_key", reference.updateEvent,
			failurecause.Attr(result.Error),
		)
	}
}

// UpdateNoticeRuns returns the update notice runs of the version with their
// template data, oldest first.
func (LegalNoticeRuns) UpdateNoticeRuns(ctx context.Context, tx *gorm.DB, kind, policyID string) ([]model.CampaignDeliveryRun, error) {
	reference, err := requireLegalNoticeReference(kind)
	if err != nil {
		return nil, err
	}
	var runs []model.CampaignDeliveryRun
	err = tx.WithContext(ctx).
		Select("id", "run_kind", "template_event_key", "template_data", "privacy_id", "terms_id").
		Where(reference.column+" = ? AND template_event_key = ?", policyID, reference.updateEvent).
		Order("id ASC").
		Find(&runs).Error
	return runs, err
}

// IsUpdateNoticePreviewToken reports whether token is the automatic preview
// link that a sealed update notice of the version mailed out. kind is
// "terms" or "privacy"; any other kind has no notices.
func (LegalNoticeRuns) IsUpdateNoticePreviewToken(ctx context.Context, db *gorm.DB, kind, policyID, token string) (bool, error) {
	reference, ok := legalNoticeReferenceOf(kind)
	if !ok {
		return false, nil
	}
	var previewURLs []string
	if err := db.WithContext(ctx).
		Table("email_delivery_run").
		Select("template_data ->> 'preview_url'").
		Where("run_kind = ?", legalNoticeRunKind).
		Where("definition_sealed = ?", true).
		Where(reference.column+" = ?", policyID).
		Where("template_event_key = ?", reference.updateEvent).
		Where("template_data ->> 'preview_url' IS NOT NULL").
		Pluck("template_data ->> 'preview_url'", &previewURLs).Error; err != nil {
		return false, err
	}
	for _, previewURL := range previewURLs {
		if previewURLHasExactToken(previewURL, token) {
			return true, nil
		}
	}
	return false, nil
}

func previewURLHasExactToken(previewURL string, token string) bool {
	parsed, err := url.Parse(strings.TrimSpace(previewURL))
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "s" {
		return false
	}
	previewToken, err := url.PathUnescape(parts[1])
	return err == nil && previewToken == token
}
