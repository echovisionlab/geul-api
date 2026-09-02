package legal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LegalNoticeActivationMode string

const (
	LegalNoticeActivationImmediate     LegalNoticeActivationMode = "immediate"
	LegalNoticeActivationScheduledDue  LegalNoticeActivationMode = "scheduled_due"
	EmailDeliveryReferenceTypeTerms                              = "terms"
	EmailDeliveryReferenceTypePrivacy                            = "privacy"
	EmailDeliveryRunKindLegalNotice                              = "legal_notice"
	CampaignDeliveryRunStatusScheduled                           = "scheduled"
	CampaignDeliveryRunStatusSending                             = "sending"
	CampaignDeliveryRunStatusSent                                = "sent"
	CampaignDeliveryRunStatusCancelled                           = "cancelled"
)

type legalNoticeActivationPolicy struct {
	entityType      string
	tableName       string
	notFoundEntity  string
	draftStatus     string
	scheduledStatus string
	activeStatus    string
	archivedStatus  string
}

// ActivateAuditedLegalNoticeDocumentWithDB is the production lifecycle
// boundary. It records the selected policy and every archived predecessor in
// the same transaction as the status transition.
func ActivateAuditedLegalNoticeDocumentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	legalOG OG,
	referenceType string,
	referenceID string,
	mode LegalNoticeActivationMode,
	now time.Time,
) (bool, error) {
	if writer == nil {
		return false, fmt.Errorf("legal policy audit writer is required")
	}
	return activateLegalNoticeDocumentWithDB(ctx, tx, writer, legalOG, referenceType, referenceID, mode, now)
}

// ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB commits an
// effective legal state only with its required sealed notice run. The caller
// dispatches the returned run after commit; mail-provider availability is not
// part of this transaction.
func ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	legalOG OG,
	notice NoticeDelivery,
	referenceType string,
	referenceID string,
	mode LegalNoticeActivationMode,
	eventKey string,
	templateData map[string]string,
	now time.Time,
) (bool, *model.CampaignDeliveryRun, error) {
	activated, err := ActivateAuditedLegalNoticeDocumentWithDB(
		ctx, tx, writer, legalOG, referenceType, referenceID, mode, now,
	)
	if err != nil || !activated {
		return activated, nil, err
	}
	if notice == nil {
		return false, nil, errs.DependencyUnavailable("legal notice delivery")
	}
	run, err := notice.CreateRun(
		ctx, tx, referenceType, referenceID, eventKey, templateData, now,
	)
	if err != nil {
		return false, nil, err
	}
	return true, run, nil
}

func activateLegalNoticeDocumentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	legalOG OG,
	referenceType string,
	referenceID string,
	mode LegalNoticeActivationMode,
	now time.Time,
) (bool, error) {
	policy, err := legalNoticeActivationPolicyForReference(referenceType)
	if err != nil {
		return false, err
	}
	if mode != LegalNoticeActivationImmediate &&
		mode != LegalNoticeActivationScheduledDue {
		return false, fmt.Errorf("unsupported legal notice activation mode %q", mode)
	}
	if legalOG == nil {
		return false, errs.DependencyUnavailable("legal OG")
	}
	if err := legalOG.LockActivation(ctx, tx, policy.entityType); err != nil {
		return false, err
	}

	var current struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
		Version       int        `gorm:"column:version"`
	}
	result := tx.WithContext(ctx).Raw(
		"SELECT status, effective_from, version FROM "+policy.tableName+" WHERE id = ? FOR UPDATE",
		referenceID,
	).Scan(&current)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		if mode == LegalNoticeActivationScheduledDue {
			return false, nil
		}
		return false, errs.NotFound(policy.notFoundEntity, referenceID)
	}

	switch mode {
	case LegalNoticeActivationImmediate:
		if current.Status != policy.draftStatus &&
			current.Status != policy.scheduledStatus {
			return false, errs.FailedPrecondition(
				"can only activate draft or scheduled " + policy.notFoundEntity,
			)
		}
	case LegalNoticeActivationScheduledDue:
		if current.Status != policy.scheduledStatus ||
			current.EffectiveFrom == nil ||
			current.EffectiveFrom.After(now) {
			return false, nil
		}
	}

	activationTime := now.UTC()
	var archived []struct {
		ID      string `gorm:"column:id"`
		Version int    `gorm:"column:version"`
	}
	if err := tx.WithContext(ctx).Raw(
		"SELECT id, version FROM "+policy.tableName+" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		referenceID, policy.activeStatus,
	).Scan(&archived).Error; err != nil {
		return false, err
	}
	for _, previous := range archived {
		if err := tx.WithContext(ctx).Table(policy.tableName).Where("id = ?", previous.ID).Updates(structured.Fields{
			"status":          policy.archivedStatus,
			"effective_until": activationTime,
			"updated_at":      activationTime,
		}).Error; err != nil {
			return false, err
		}
	}

	updates := structured.Fields{
		"status":     policy.activeStatus,
		"updated_at": activationTime,
	}
	var allowedCurrentStatuses []string
	if mode == LegalNoticeActivationImmediate {
		updates["effective_from"] = activationTime
		allowedCurrentStatuses = []string{
			policy.draftStatus,
			policy.scheduledStatus,
		}
	} else {
		allowedCurrentStatuses = []string{policy.scheduledStatus}
	}
	activation := tx.WithContext(ctx).
		Table(policy.tableName).
		Where("id = ? AND status IN ?", referenceID, allowedCurrentStatuses).
		Updates(updates)
	if activation.Error != nil {
		return false, activation.Error
	}
	if activation.RowsAffected != 1 {
		return false, gorm.ErrRecordNotFound
	}
	if writer == nil {
		return true, nil
	}
	var systemService *sharedtelemetry.ServiceName
	if mode == LegalNoticeActivationScheduledDue {
		serviceName := sharedtelemetry.ServiceBackend
		systemService = &serviceName
	}
	for _, previous := range archived {
		if err := appendLegalPolicyLifecycleAudit(
			ctx, tx, writer, policy.entityType, previous.ID, previous.Version,
			[]string{"effective_at", "status"}, sharedtelemetry.AuditStateActive, sharedtelemetry.AuditStateArchived,
			&activationTime, systemService,
		); err != nil {
			return false, err
		}
	}
	changedFields := []string{"status"}
	var effectiveAt *time.Time
	if mode == LegalNoticeActivationImmediate {
		changedFields = []string{"effective_at", "status"}
		effectiveAt = &activationTime
	}
	if err := appendLegalPolicyLifecycleAudit(
		ctx, tx, writer, policy.entityType, referenceID, current.Version,
		changedFields, legalPolicyAuditState(current.Status), sharedtelemetry.AuditStateActive,
		effectiveAt, systemService,
	); err != nil {
		return false, err
	}
	return true, nil
}

func legalPolicyAuditState(status string) sharedtelemetry.AuditState {
	switch status {
	case managev1.TermsStatus_TERMS_STATUS_DRAFT.String(), managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String():
		return sharedtelemetry.AuditStateDraft
	case managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(), managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String():
		return sharedtelemetry.AuditStateScheduled
	case managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String():
		return sharedtelemetry.AuditStateActive
	case managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(), managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String():
		return sharedtelemetry.AuditStateArchived
	default:
		return ""
	}
}

func legalNoticeActivationPolicyForReference(
	referenceType string,
) (legalNoticeActivationPolicy, error) {
	switch referenceType {
	case EmailDeliveryReferenceTypePrivacy:
		return legalNoticeActivationPolicy{
			entityType:      "privacy",
			tableName:       "privacy_history",
			notFoundEntity:  "privacy policy",
			draftStatus:     managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(),
			scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			activeStatus:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
		}, nil
	case EmailDeliveryReferenceTypeTerms:
		return legalNoticeActivationPolicy{
			entityType:      "terms",
			tableName:       "terms_history",
			notFoundEntity:  "terms",
			draftStatus:     managev1.TermsStatus_TERMS_STATUS_DRAFT.String(),
			scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			activeStatus:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
		}, nil
	default:
		return legalNoticeActivationPolicy{}, fmt.Errorf(
			"unsupported legal notice reference type %q",
			referenceType,
		)
	}
}

func EnsureLegalNoticeScheduleSlotWithDB(
	ctx context.Context,
	tx *gorm.DB,
	referenceType string,
	referenceID string,
) error {
	policy, err := legalNoticeActivationPolicyForReference(referenceType)
	if err != nil {
		return err
	}
	var existingIDs []string
	if err := tx.WithContext(ctx).Raw(
		"SELECT id FROM "+policy.tableName+
			" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		referenceID,
		policy.scheduledStatus,
	).Scan(&existingIDs).Error; err != nil {
		return err
	}
	if len(existingIDs) > 0 {
		return errs.FailedPrecondition(
			"another " + policy.notFoundEntity + " version is already scheduled",
		)
	}
	return nil
}

func dispatchCreatedLegalNoticeRun(
	ctx context.Context,
	notice NoticeDelivery,
	run *model.CampaignDeliveryRun,
) {
	if run == nil || notice == nil {
		return
	}
	if err := notice.DispatchRun(ctx, run.ID); err != nil {
		slog.Warn("legal notice delivery run dispatch will be retried by scheduler",
			"run_id", run.ID,
			"run_kind", run.RunKind,
			"error", err,
		)
	}
}

// DispatchCommittedLegalEffectiveNoticeAfterActivation performs only
// best-effort mail dispatch after the activation transaction has already
// sealed its required effective-notice run. Provider/PGMQ delivery remains
// independent of the policy's committed legal effect.
func DispatchCommittedLegalEffectiveNoticeAfterActivation(
	ctx context.Context,
	db *gorm.DB,
	notice NoticeDelivery,
	referenceType string,
	referenceID string,
	now time.Time,
	run *model.CampaignDeliveryRun,
) {
	cancelScheduledLegalUpdateNoticeAfterActivation(ctx, db, referenceType, referenceID, now)
	dispatchCreatedLegalNoticeRun(ctx, notice, run)
}

// cancelScheduledLegalUpdateNoticeAfterActivation prevents the dispatcher from
// selecting an obsolete pre-effective update after the policy is already
// active. The policy commit is authoritative: cleanup is best-effort, and a
// concurrently sending or terminal run is never overwritten.
func cancelScheduledLegalUpdateNoticeAfterActivation(
	ctx context.Context,
	db *gorm.DB,
	referenceType string,
	referenceID string,
	now time.Time,
) {
	referenceColumn := ""
	eventKey := ""
	switch referenceType {
	case EmailDeliveryReferenceTypeTerms:
		referenceColumn = "terms_id"
		eventKey = email.EventTermsUpdate.String()
	case EmailDeliveryReferenceTypePrivacy:
		referenceColumn = "privacy_id"
		eventKey = email.EventPrivacyUpdate.String()
	default:
		slog.Warn(
			"legal policy activation committed without update notice cleanup",
			"reference_type", referenceType,
			"reference_id", referenceID,
			"error", "unsupported legal notice reference type",
		)
		return
	}

	result := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("run_kind = ?", EmailDeliveryRunKindLegalNotice).
		Where(referenceColumn+" = ?", referenceID).
		Where("template_event_key = ?", eventKey).
		Where("status = ?", CampaignDeliveryRunStatusScheduled).
		Updates(structured.Fields{
			"status":       CampaignDeliveryRunStatusCancelled,
			"completed_at": now.UTC(),
		})
	if result.Error != nil {
		slog.Warn(
			"legal policy activation committed without update notice cleanup",
			"reference_type", referenceType,
			"reference_id", referenceID,
			"event_key", eventKey,
			"error", result.Error,
		)
	}
}

func cancelActiveLegalNoticeDeliveryRuns(
	ctx context.Context,
	tx *gorm.DB,
	referenceType string,
	referenceID string,
) error {
	referenceColumn := ""
	switch referenceType {
	case EmailDeliveryReferenceTypeTerms:
		referenceColumn = "terms_id"
	case EmailDeliveryReferenceTypePrivacy:
		referenceColumn = "privacy_id"
	default:
		return errs.InvalidArgumentMsg(
			"unsupported legal notice reference type",
		)
	}

	var activeRuns []model.CampaignDeliveryRun
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			referenceColumn+" = ? AND status IN ?",
			referenceID,
			[]string{
				CampaignDeliveryRunStatusScheduled,
				CampaignDeliveryRunStatusSending,
				CampaignDeliveryRunStatusSent,
			},
		).
		Order("id ASC").
		Find(&activeRuns).Error; err != nil {
		return err
	}
	for i := range activeRuns {
		if activeRuns[i].Status == CampaignDeliveryRunStatusSending ||
			activeRuns[i].Status == CampaignDeliveryRunStatusSent {
			return errs.FailedPrecondition(
				"legal notice delivery has already started",
			)
		}
	}

	now := time.Now().UTC()
	return tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where(
			referenceColumn+" = ? AND status = ?",
			referenceID,
			CampaignDeliveryRunStatusScheduled,
		).
		Updates(structured.Fields{
			"status":       CampaignDeliveryRunStatusCancelled,
			"completed_at": now,
		}).Error
}
