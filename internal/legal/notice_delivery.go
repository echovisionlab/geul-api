package legal

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// Campaign delivery values the legal notices of both kinds use.
const (
	EmailDeliveryReferenceTypeTerms    = "terms"
	EmailDeliveryReferenceTypePrivacy  = "privacy"
	EmailDeliveryRunKindLegalNotice    = "legal_notice"
	CampaignDeliveryRunStatusScheduled = "scheduled"
	CampaignDeliveryRunStatusSending   = "sending"
	CampaignDeliveryRunStatusSent      = "sent"
	CampaignDeliveryRunStatusCancelled = "cancelled"
)

// ActivateDuePolicyWithEffectiveRun activates the kind's scheduled version
// referenceID once its effective time has come, archives the active one, and
// seals the required effective notice run in the same transaction; the
// backend is the actor of the audit records. It reports false, without an
// error, when the version is gone or not due. The caller dispatches the
// returned run after commit, so mail-provider availability is not part of
// this transaction.
func ActivateDuePolicyWithEffectiveRun(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	legalOG OG,
	notice NoticeDelivery,
	referenceType string,
	referenceID string,
	eventKey string,
	templateData map[string]string,
	now time.Time,
) (bool, *model.CampaignDeliveryRun, error) {
	kind, err := PolicyKind(referenceType).descriptor()
	if err != nil {
		return false, nil, err
	}
	activation, activated, err := activatePolicyVersion(ctx, tx, legalOG, kind, referenceID, false, now)
	if err != nil || !activated {
		return false, nil, err
	}
	for _, change := range activation.lifecycleChanges() {
		if err := domainaudit.AppendSystem(ctx, tx, writer, sharedtelemetry.ServiceBackend, sharedtelemetry.AuditLegalPolicyUpdated, change.record); err != nil {
			return false, nil, err
		}
	}
	run, err := notice.CreateRun(ctx, tx, referenceType, referenceID, eventKey, templateData, now)
	if err != nil {
		return false, nil, err
	}
	return true, run, nil
}

// policyActivation is what activating one version changed.
type policyActivation struct {
	kind      *policyKind
	policyID  string
	version   int
	previous  model.LegalPolicyStatus
	immediate bool
	at        time.Time
	archived  []archivedPolicyVersion
}

type archivedPolicyVersion struct {
	ID      string `gorm:"column:id"`
	Version int    `gorm:"column:version"`
}

type activationTarget struct {
	Status        model.LegalPolicyStatus `gorm:"column:status"`
	EffectiveFrom *time.Time              `gorm:"column:effective_from"`
	Version       int                     `gorm:"column:version"`
}

// activatePolicyVersion makes policyID the kind's active version under the
// kind's activation lock and archives the version that was active. An
// immediate activation takes a draft or scheduled version and starts it now;
// otherwise only a scheduled version whose effective time has come
// activates, and a version that is gone or not due reports false.
func activatePolicyVersion(
	ctx context.Context,
	tx *gorm.DB,
	legalOG OG,
	kind *policyKind,
	policyID string,
	immediate bool,
	now time.Time,
) (policyActivation, bool, error) {
	if err := legalOG.LockActivation(ctx, tx, kind.entity()); err != nil {
		return policyActivation{}, false, err
	}
	var current activationTarget
	result := tx.WithContext(ctx).Raw(
		"SELECT status, effective_from, version FROM "+kind.table+" WHERE id = ? FOR UPDATE", policyID,
	).Scan(&current)
	if result.Error != nil {
		return policyActivation{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		if immediate {
			return policyActivation{}, false, domainerr.ResourceNotFound(kind.notFoundResource, policyID)
		}
		return policyActivation{}, false, nil
	}
	if ready, err := kind.activatable(current, immediate, now); !ready || err != nil {
		return policyActivation{}, false, err
	}
	activation := policyActivation{
		kind: kind, policyID: policyID, version: current.Version, previous: current.Status,
		immediate: immediate, at: now.UTC(),
	}
	if err := activation.archiveActive(ctx, tx); err != nil {
		return policyActivation{}, false, err
	}
	if err := activation.activate(ctx, tx); err != nil {
		return policyActivation{}, false, err
	}
	return activation, true, nil
}

// activatable reports whether current may activate now. An immediate
// activation of a version that is neither draft nor scheduled is an error; a
// due activation of a version that is not due is not.
func (k *policyKind) activatable(current activationTarget, immediate bool, now time.Time) (bool, error) {
	if immediate {
		if current.Status != k.draft && current.Status != k.scheduled {
			return false, domainerr.Precondition("can only activate draft or scheduled " + k.notFoundResource)
		}
		return true, nil
	}
	return current.Status == k.scheduled && current.EffectiveFrom != nil && !current.EffectiveFrom.After(now), nil
}

// archiveActive archives every other active version, in ID order.
func (a *policyActivation) archiveActive(ctx context.Context, tx *gorm.DB) error {
	if err := tx.WithContext(ctx).Raw(
		"SELECT id, version FROM "+a.kind.table+" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		a.policyID, a.kind.active,
	).Scan(&a.archived).Error; err != nil {
		return err
	}
	for _, previous := range a.archived {
		if err := tx.WithContext(ctx).Table(a.kind.table).Where("id = ?", previous.ID).Updates(structured.Fields{
			"status": a.kind.archived, "effective_until": a.at, "updated_at": a.at,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a policyActivation) activate(ctx context.Context, tx *gorm.DB) error {
	updates := structured.Fields{"status": a.kind.active, "updated_at": a.at}
	allowed := []model.LegalPolicyStatus{a.kind.scheduled}
	if a.immediate {
		updates["effective_from"] = a.at
		allowed = []model.LegalPolicyStatus{a.kind.draft, a.kind.scheduled}
	}
	activation := tx.WithContext(ctx).Table(a.kind.table).
		Where("id = ? AND status IN ?", a.policyID, allowed).
		Updates(updates)
	if activation.Error != nil {
		return activation.Error
	}
	if activation.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// lifecycleChanges returns the lifecycle record of every archived
// predecessor, then of the activated version.
func (a policyActivation) lifecycleChanges() []legalLifecycleChange {
	changes := make([]legalLifecycleChange, 0, len(a.archived)+1)
	for _, previous := range a.archived {
		changes = append(changes, legalLifecycleChange{
			kind: a.kind.entity(), policyID: previous.ID, version: previous.Version,
			changedFields: []string{"effective_at", "status"},
			previous:      sharedtelemetry.AuditStateActive, next: sharedtelemetry.AuditStateArchived,
			effectiveAt: &a.at,
		})
	}
	activated := legalLifecycleChange{
		kind: a.kind.entity(), policyID: a.policyID, version: a.version,
		changedFields: []string{"status"},
		previous:      a.kind.auditState(a.previous), next: sharedtelemetry.AuditStateActive,
	}
	if a.immediate {
		activated.changedFields = []string{"effective_at", "status"}
		activated.effectiveAt = &a.at
	}
	return append(changes, activated)
}

// ensureScheduleSlot rejects scheduling policyID while another version of
// the kind is scheduled.
func ensureScheduleSlot(ctx context.Context, tx *gorm.DB, kind *policyKind, policyID string) error {
	var existingIDs []string
	if err := tx.WithContext(ctx).Raw(
		"SELECT id FROM "+kind.table+" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		policyID, kind.scheduled,
	).Scan(&existingIDs).Error; err != nil {
		return err
	}
	if len(existingIDs) > 0 {
		return domainerr.Precondition("another " + kind.notFoundResource + " version is already scheduled")
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
		slog.WarnContext(ctx, "Legal notice delivery run dispatch failed; the scheduler will retry it",
			"run_id", run.ID,
			"run_kind", run.RunKind,
			failurecause.Attr(err),
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
	notice.CancelUpdateNoticeAfterActivation(ctx, db, referenceType, referenceID, now)
	dispatchCreatedLegalNoticeRun(ctx, notice, run)
}
