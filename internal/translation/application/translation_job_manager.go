package application

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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
	PublishContentUpdatedWithExecutor(context.Context, eventpkg.DBTX, *managev1.ContentUpdatedEvent) error
}

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
}

const translationTerminalCleanupTimeout = 5 * time.Second

type TranslationJobManagerOption func(*TranslationJobManager)

func WithTranslationJobContentBlockStore(store *contentblock.Store) TranslationJobManagerOption {
	return func(manager *TranslationJobManager) { manager.contentBlocks = store }
}

func WithTranslationJobDomainRegistry(registry DomainRegistry) TranslationJobManagerOption {
	return func(manager *TranslationJobManager) { manager.domains = registry }
}

func NewTranslationJobManager(
	db *gorm.DB,
	publisher translationJobPublisher,
	ogPlanner *og.Planner,
	ogRefresher *og.Refresher,
	options ...TranslationJobManagerOption,
) (*TranslationJobManager, error) {
	dependencycheck.New("TranslationJobManager").
		RequireNotNil(db, "db").
		RequireNotNil(publisher, "publisher").
		RequireNotNil(ogPlanner, "og planner").
		RequireNotNil(ogRefresher, "og refresher").
		Validate()

	manager := &TranslationJobManager{
		db:                db,
		generatorResolver: newDBTranslationGeneratorResolver(db),
		publisher:         publisher,
		now:               time.Now,
		metrics:           newTranslationMetrics(),
		ogPlanner:         ogPlanner,
		ogRefresher:       ogRefresher,
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager, nil
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
