package application

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// newTestService builds the Translation use cases through NewService.
// Fields left unset in deps get stand-ins: an unconfigured SpiceDB client,
// a recording audit appender, the test domain registry and XLIFF ports that
// fail if a test reaches them.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.DB == nil {
		deps.DB = &gorm.DB{}
	}
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	if deps.Publisher == nil {
		deps.Publisher = unusedTranslationPublisher{}
	}
	if deps.ContentBlocks == nil {
		deps.ContentBlocks = &contentblock.Store{}
	}
	if deps.OGPlanner == nil {
		deps.OGPlanner = &ogpostgres.Planner{}
	}
	if deps.OGRefresher == nil {
		deps.OGRefresher = &ogpostgres.Refresher{}
	}
	if deps.Domains == nil {
		deps.Domains = testTranslationDomains{}
	}
	if deps.XLIFFFiles == nil {
		deps.XLIFFFiles = unusedXLIFFFiles{}
	}
	if deps.InterchangeDomains == nil {
		deps.InterchangeDomains = unusedInterchangeDomains{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

var errUnusedTestDependency = errors.New("test did not configure this Translation dependency")

type unusedTranslationPublisher struct{}

func (unusedTranslationPublisher) EnqueueTranslationGenerateWithDB(context.Context, *gorm.DB, *managev1.TranslationGenerateEvent) error {
	return errUnusedTestDependency
}

func (unusedTranslationPublisher) PublishTranslationLifecycle(context.Context, *managev1.TranslationLifecycleEvent) error {
	return errUnusedTestDependency
}

func (unusedTranslationPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return errUnusedTestDependency
}

type unusedXLIFFFiles struct{}

func (unusedXLIFFFiles) CreateTranslationXLIFF(context.Context, TranslationXLIFFArtifact) (*commonv1.ExpiringMediaRef, error) {
	return nil, errUnusedTestDependency
}

func (unusedXLIFFFiles) ReadVerifiedTranslationXLIFF(context.Context, string, int64) (VerifiedTranslationXLIFF, error) {
	return VerifiedTranslationXLIFF{}, errUnusedTestDependency
}

type unusedInterchangeDomains struct{}

func (unusedInterchangeDomains) LoadTranslationInterchangeTarget(context.Context, *gorm.DB, *contentblock.Store, string, string, string, *translation.ExtractionPlan) (TranslationInterchangeTargetState, error) {
	return TranslationInterchangeTargetState{}, errUnusedTestDependency
}

func (unusedInterchangeDomains) ApplyTranslationInterchange(context.Context, *gorm.DB, *contentblock.Store, TranslationInterchangeApply) (TranslationInterchangeApplyResult, error) {
	return TranslationInterchangeApplyResult{}, errUnusedTestDependency
}

// withTestMutations gives a job manager built as a literal the mutation
// runner NewTranslationJobManager would, over the manager's database.
func withTestMutations(t *testing.T, manager *TranslationJobManager) *TranslationJobManager {
	t.Helper()
	db := manager.db
	if db == nil {
		db = &gorm.DB{}
	}
	mutations, err := mutation.NewRunner(db, &auth.SpiceDBClient{}, identitystate.PostgresFence{}, &audittest.Recorder{})
	require.NoError(t, err)
	manager.mutations = mutations
	return manager
}
