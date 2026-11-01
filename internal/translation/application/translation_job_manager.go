package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

var errTranslationJobNoLongerCurrent = errors.New("translation job is no longer current")
var errTranslationOgHandoffFailed = errors.New("translation OG handoff failed")

// LifecycleStatus is the terminal outcome a lifecycle event announces,
// named as the event contract names it.
type LifecycleStatus string

const (
	LifecycleApplied LifecycleStatus = "TRANSLATION_LIFECYCLE_STATUS_APPLIED"
	LifecycleFailed  LifecycleStatus = "TRANSLATION_LIFECYCLE_STATUS_FAILED"
)

// LifecycleEvent announces that a job reached a terminal outcome.
// FailureReason is the bounded failure classification of a failed job.
type LifecycleEvent struct {
	JobID         string
	EntityType    string
	EntityID      string
	TargetLocale  string
	Status        LifecycleStatus
	FailureReason *string
	Timestamp     time.Time
}

// LifecyclePublisher announces job outcomes after the job's row is gone; a
// failed publish is logged, never retried.
type LifecyclePublisher interface {
	PublishTranslationLifecycle(ctx context.Context, event LifecycleEvent) error
}

// TranslationJobManager runs queued translation jobs: it claims a job,
// generates the target with the active providers and applies the result
// through the owning domain.
type TranslationJobManager struct {
	units             mutation.UnitOfWork[JobStore]
	generatorResolver TranslationGeneratorResolver
	lifecycle         LifecyclePublisher
	now               func() time.Time
	metrics           translationMetrics
	plans             DomainPlans
	mutations         *mutation.StoreRunner[JobStore]
}

const translationTerminalCleanupTimeout = 5 * time.Second

// JobManagerDeps are the collaborators of the translation job runner; all
// are required.
type JobManagerDeps struct {
	Units     mutation.UnitOfWork[JobStore]
	SpiceDB   mutation.Authorizer
	Fence     identitystate.Fence
	Audit     domainaudit.Appender
	Plans     DomainPlans
	Lifecycle LifecyclePublisher
}

// NewTranslationJobManager returns the job runner over deps.
func NewTranslationJobManager(deps JobManagerDeps) (*TranslationJobManager, error) {
	if err := requireDeps("translation job manager", map[string]bool{
		"Units": deps.Units == nil, "SpiceDB": deps.SpiceDB == nil, "Fence": deps.Fence == nil,
		"Audit": deps.Audit == nil, "Plans": deps.Plans == nil, "Lifecycle": deps.Lifecycle == nil,
	}); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &TranslationJobManager{
		units:             deps.Units,
		generatorResolver: storeTranslationGeneratorResolver{units: deps.Units},
		lifecycle:         deps.Lifecycle,
		now:               dbtime.Now,
		metrics:           newTranslationMetrics(),
		plans:             deps.Plans,
		mutations:         mutations,
	}, nil
}

// runningJobAccess authorizes a runner step by the job it continues: the
// request that queued the job was authorized, and the job must still be
// running. Requester identity, relationships and lifecycle are intentionally
// not re-authorized.
func runningJobAccess(jobID string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, store JobStore) error {
		return lockRunningTranslationJob(ctx, store, jobID)
	})
}

// queuedJobAccess authorizes a runner update whose own condition on the
// job's row decides it: the request that queued the job was authorized.
var queuedJobAccess = mutation.Checked(func(context.Context, JobStore) error { return nil })

// runJobUpdate runs one conditional update of a job's row, which answers
// whether its condition still held.
func (m *TranslationJobManager) runJobUpdate(ctx context.Context, update func(context.Context, JobStore) (bool, error)) (bool, error) {
	return mutation.Run(ctx, m.mutations, mutation.StoreSpec[bool, JobStore]{
		Access: queuedJobAccess,
		Change: update,
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(bool) mutation.Audit { return mutation.Unchanged() },
	})
}

func (m *TranslationJobManager) resolveGenerators(ctx context.Context) ([]translation.Generator, error) {
	if m.generatorResolver == nil {
		return nil, errTranslationProviderUnavailable
	}
	return m.generatorResolver.ResolveAll(ctx)
}

func (m *TranslationJobManager) ProcessJob(ctx context.Context, jobID string) error {
	return m.ProcessDelivery(ctx, jobID)
}
