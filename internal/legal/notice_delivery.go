package legal

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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

// policyActivation is what activating one version changed.
type policyActivation struct {
	kind      *policyKind
	policyID  string
	version   int
	previous  Status
	immediate bool
	at        time.Time
	archived  []VersionRef
}

// activatePolicyVersion makes policyID the kind's active version under the
// kind's activation lock and archives the version that was active. An
// immediate activation takes a draft or scheduled version and starts it now;
// otherwise only a scheduled version whose effective time has come
// activates, and a version that is gone or not due reports false.
func activatePolicyVersion(
	ctx context.Context,
	store Store,
	kind *policyKind,
	policyID string,
	immediate bool,
	now time.Time,
) (policyActivation, bool, error) {
	if err := store.LockActivation(ctx, kind.kind); err != nil {
		return policyActivation{}, false, err
	}
	current, found, err := store.LockLifecycle(ctx, kind.kind, policyID)
	if err != nil {
		return policyActivation{}, false, err
	}
	if !found {
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
	if err := activation.archiveActive(ctx, store); err != nil {
		return policyActivation{}, false, err
	}
	if err := activation.activate(ctx, store); err != nil {
		return policyActivation{}, false, err
	}
	return activation, true, nil
}

// activatable reports whether current may activate now. An immediate
// activation of a version that is neither draft nor scheduled is an error; a
// due activation of a version that is not due is not.
func (k *policyKind) activatable(current Lifecycle, immediate bool, now time.Time) (bool, error) {
	if immediate {
		if current.Status != k.draft && current.Status != k.scheduled {
			return false, domainerr.Precondition("can only activate draft or scheduled " + k.notFoundResource)
		}
		return true, nil
	}
	return current.Status == k.scheduled && current.EffectiveFrom != nil && !current.EffectiveFrom.After(now), nil
}

// archiveActive archives every other active version, in ID order.
func (a *policyActivation) archiveActive(ctx context.Context, store Store) error {
	var err error
	if a.archived, err = store.LockActiveVersions(ctx, a.kind.kind, a.policyID); err != nil {
		return err
	}
	for _, previous := range a.archived {
		if err := store.Archive(ctx, a.kind.kind, previous.ID, a.at); err != nil {
			return err
		}
	}
	return nil
}

// activate makes the version active: a scheduled one at its effective
// time, or an immediate draft or scheduled one from now.
func (a policyActivation) activate(ctx context.Context, store Store) error {
	from := []Status{a.kind.scheduled}
	var effectiveFrom *time.Time
	if a.immediate {
		effectiveFrom = &a.at
		from = []Status{a.kind.draft, a.kind.scheduled}
	}
	return store.Activate(ctx, a.kind.kind, a.policyID, from, effectiveFrom, a.at)
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
func ensureScheduleSlot(ctx context.Context, store Store, kind *policyKind, policyID string) error {
	existingIDs, err := store.LockScheduledIDs(ctx, kind.kind, policyID)
	if err != nil {
		return err
	}
	if len(existingIDs) > 0 {
		return domainerr.Precondition("another " + kind.notFoundResource + " version is already scheduled")
	}
	return nil
}

func dispatchCreatedLegalNoticeRun(
	ctx context.Context,
	dispatch NoticeDispatch,
	run *NoticeRun,
) {
	if run == nil || dispatch == nil {
		return
	}
	if err := dispatch.DispatchRun(ctx, run.ID); err != nil {
		slog.WarnContext(ctx, "Legal notice delivery run dispatch failed; the scheduler will retry it",
			"run_id", run.ID,
			"run_kind", run.RunKind,
			failurecause.Attr(err),
		)
	}
}

// DispatchCommittedEffectiveNotice performs only best-effort mail dispatch
// after the activation transaction has already sealed its required
// effective-notice run. Provider/PGMQ delivery remains independent of the
// policy's committed legal effect.
func DispatchCommittedEffectiveNotice(
	ctx context.Context,
	dispatch NoticeDispatch,
	kind PolicyKind,
	id string,
	now time.Time,
	run *NoticeRun,
) {
	dispatch.CancelUpdateNoticeAfterActivation(ctx, kind, id, now)
	dispatchCreatedLegalNoticeRun(ctx, dispatch, run)
}
