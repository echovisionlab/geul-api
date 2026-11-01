package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

const (
	translationJobStatusQueued    = "queued"
	translationJobStatusRunning   = "running"
	translationJobStatusApplied   = "applied"
	translationJobStatusFailed    = "failed"
	translationJobStatusCancelled = "cancelled"
)

var errTranslationJobNoLongerCurrent = errors.New("translation job is no longer current")
var errTranslationOgHandoffFailed = errors.New("translation OG handoff failed")

type translationJobPublisher interface {
	asyncPublisher
	PublishTranslationGenerate(ctx context.Context, job *managev1.TranslationGenerateEvent) error
	PublishTranslationLifecycle(ctx context.Context, event *managev1.TranslationLifecycleEvent) error
	// content.updated is scheduled only in the owning apply transaction.
	contentsignal.Publisher
}

// TranslationJobManager runs queued translation jobs: it claims a job,
// generates the target with the active providers and applies the result
// through the owning domain.
type TranslationJobManager struct {
	db                *gorm.DB
	generatorResolver TranslationGeneratorResolver
	publisher         translationJobPublisher
	now               func() time.Time
	metrics           translationMetrics
	contentBlocks     *contentblock.Store
	ogPlanner         *og.Planner
	ogRefresher       *og.Refresher
	domains           DomainRegistry
	mutations         *mutation.Runner
}

const translationTerminalCleanupTimeout = 5 * time.Second

// JobManagerDeps are the collaborators of the translation job runner; all
// are required.
type JobManagerDeps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	Publisher     translationJobPublisher
	ContentBlocks *contentblock.Store
	OGPlanner     *og.Planner
	OGRefresher   *og.Refresher
	Domains       DomainRegistry
}

// NewTranslationJobManager returns the job runner over deps.
func NewTranslationJobManager(deps JobManagerDeps) (*TranslationJobManager, error) {
	if err := requireDeps("translation job manager", map[string]bool{
		"DB": deps.DB == nil, "SpiceDB": deps.SpiceDB == nil, "Audit": deps.Audit == nil,
		"Publisher": deps.Publisher == nil, "ContentBlocks": deps.ContentBlocks == nil,
		"OGPlanner": deps.OGPlanner == nil, "OGRefresher": deps.OGRefresher == nil, "Domains": deps.Domains == nil,
	}); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &TranslationJobManager{
		db:                deps.DB,
		generatorResolver: newDBTranslationGeneratorResolver(deps.DB),
		publisher:         deps.Publisher,
		now:               dbtime.Now,
		metrics:           newTranslationMetrics(),
		contentBlocks:     deps.ContentBlocks,
		ogPlanner:         deps.OGPlanner,
		ogRefresher:       deps.OGRefresher,
		domains:           deps.Domains,
		mutations:         mutations,
	}, nil
}

// runningJobAccess authorizes a runner step by the job it continues: the
// request that queued the job was authorized, and the job must still be
// running. Requester identity, relationships and lifecycle are intentionally
// not re-authorized.
func runningJobAccess(jobID string) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return lockRunningTranslationJob(ctx, tx, jobID)
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
