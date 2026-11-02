package legal

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ScheduledActivator activates due policy versions on the backend's behalf.
type ScheduledActivator struct{ mutations *mutation.StoreRunner[Store] }

func NewScheduledActivator(units mutation.UnitOfWork[Store], audit domainaudit.Appender) (*ScheduledActivator, error) {
	runner, err := mutation.NewSystemStoreRunner(units, audit)
	if err != nil {
		return nil, err
	}
	return &ScheduledActivator{mutations: runner}, nil
}

type scheduledActivation struct {
	activation policyActivation
	activated  bool
	run        *NoticeRun
}

// ActivateDuePolicy activates a due scheduled version, archives its predecessor
// and seals the effective notice run after the system audit. A missing or not-due
// version changes nothing. The caller dispatches the returned run after commit.
func (s *ScheduledActivator) ActivateDuePolicy(ctx context.Context, kind PolicyKind, id, eventKey string, templateData map[string]string, now time.Time) (bool, *NoticeRun, error) {
	descriptor, err := kind.descriptor()
	if err != nil {
		return false, nil, err
	}
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[*scheduledActivation, Store]{
		Access: mutation.System(),
		Change: func(ctx context.Context, store Store) (*scheduledActivation, error) {
			activation, activated, err := activatePolicyVersion(ctx, store, descriptor, id, false, now)
			if err != nil {
				return nil, err
			}
			return &scheduledActivation{activation: activation, activated: activated}, nil
		},
		Audit: func(result *scheduledActivation) mutation.Audit {
			if !result.activated {
				return mutation.Unchanged()
			}
			changes := result.activation.lifecycleChanges()
			records := make([]mutation.Audit, 0, len(changes))
			for _, change := range changes {
				records = append(records, mutation.SystemAudit(sharedtelemetry.ServiceBackend, sharedtelemetry.AuditLegalPolicyUpdated, change.record))
			}
			return mutation.Audits(records...)
		},
		Outbox: func(ctx context.Context, store Store, result *scheduledActivation) error {
			if !result.activated {
				return nil
			}
			var err error
			result.run, err = store.SealNoticeRun(ctx, kind, id, eventKey, templateData, now)
			return err
		},
	})
	if err != nil {
		return false, nil, err
	}
	return result.activated, result.run, nil
}
