package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type sourceLocaleSwitchDeliveryDomains struct {
	testTranslationDomains
	source *translation.SourceDocument
}

func (d sourceLocaleSwitchDeliveryDomains) LoadSourceDocument(
	context.Context,
	*gorm.DB,
	*contentblock.Store,
	string,
	string,
) (*translation.SourceDocument, error) {
	return d.source, nil
}

func (sourceLocaleSwitchDeliveryDomains) BuildExtractionPlan(
	*model.TranslationJob,
	*translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	return nil, errors.New("late delivery must not re-extract current source text")
}

func TestReloadTranslationDeliveryKeepsRequestPlanAcrossSourceLocaleSwitch(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	plan := &translation.ExtractionPlan{
		EntityType: job.EntityType, EntityID: job.EntityID,
		SourceLocale: "en", TargetLocale: job.TargetLocale,
		Units: []translation.Unit{
			{UnitID: "entity:title", SourceLocale: "en"},
			{UnitID: "block:deleted:typed:paragraph/content", SourceLocale: "en"},
		},
	}
	current := &translation.SourceDocument{SourceLocale: "ko", Title: ""}
	manager := &TranslationJobManager{
		db: db, domains: sourceLocaleSwitchDeliveryDomains{source: current},
		publisher: stubTranslationJobPublisher{}, now: time.Now,
		metrics: newTranslationMetrics(),
	}
	execution := &translationDeliveryExecution{job: job, plan: plan}

	proceed, err := manager.reloadTranslationDeliveryForApply(context.Background(), execution)
	require.NoError(t, err)
	require.True(t, proceed)
	require.Same(t, current, execution.sourceDoc)
	require.Same(t, plan, execution.plan)
	require.Equal(t, "en", execution.plan.SourceLocale)
	require.Len(t, execution.plan.Units, 2)
}

func TestTranslationRunningProviderFailureIsImmediatelyTerminal(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_100, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db:        db,
		publisher: stubTranslationJobPublisher{},
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
	}
	cause := errors.New("provider unavailable")

	err := manager.handleRunningDeliveryFailure(context.Background(), job, now, cause)
	require.Error(t, err)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "translation_generation_failed", class)

	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestValidatedTranslationKeepsProviderResponseInMemoryUntilTerminal(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "response-artifact", uuid.NewString())
	request := translationJobProviderRequest(translation.XLIFFUnit{ID: "unit-1", Source: "Hello"})
	response := translationJobProviderResponse(
		request,
		translation.UnitResult{UnitID: "unit-1", TranslatedText: "Bonjour"},
	)
	manager := &TranslationJobManager{db: db}
	generator := &scriptedTranslationGenerator{session: &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{response},
	}}

	validated, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(), job, request, generator,
	)
	require.NoError(t, err)
	require.NotNil(t, validated)
	require.Equal(t, response.Document, validated.Document)
	require.NoError(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error)
	require.NoError(t, manager.finishJob(
		context.Background(), job, translationJobStatusApplied, nil, time.Unix(1_700_000_200, 0).UTC(),
	))
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestInvalidTranslationDoesNotPersistProviderResponse(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "invalid-response-artifact", uuid.NewString())
	request := translationJobProviderRequest(translation.XLIFFUnit{ID: "unit-1", Source: "Hello\nworld"})
	response := translationJobProviderResponse(
		request,
		translation.UnitResult{UnitID: "unit-1", TranslatedText: "Bonjour monde"},
	)
	manager := &TranslationJobManager{db: db}
	generator := &scriptedTranslationGenerator{session: &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{response},
	}}

	_, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(), job, request, generator,
	)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	var stored model.TranslationJob
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, translationJobStatusRunning, stored.Status)
}

func TestTranslationRunningTargetApplyFailureUsesBoundedTerminalReason(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_125, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db:        db,
		publisher: stubTranslationJobPublisher{},
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
	}
	cause := markTranslationTargetApplyFailure(errors.New("sensitive target persistence detail"))

	err := manager.handleRunningDeliveryFailure(context.Background(), job, now, cause)
	require.Error(t, err)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "translation_generation_failed", class)
	require.NotContains(t, err.Error(), "sensitive target persistence detail")

	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationCandidateBuildFailureUsesTargetApplyReason(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_140, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	document := testPostVersionLocalizedDocument()
	document.LocaleOverlay.Blocks[0].GetParagraph().Content = append(
		document.LocaleOverlay.Blocks[0].GetParagraph().Content,
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{
			Text: &contentv1.RichTextStyledText{Text: " world"},
		}},
	)
	source := &translation.SourceDocument{ContentBlockDocument: document}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 1)
	response := translationProviderResponseForPlanTest(t, plan, translation.UnitResult{
		UnitID: plan.Units[0].UnitID, TranslatedText: "안녕하세요 세계",
	})
	manager := &TranslationJobManager{
		db:        db,
		publisher: stubTranslationJobPublisher{},
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
	}

	err = manager.applyTranslationDelivery(context.Background(), &translationDeliveryExecution{
		job: job, startedAt: now, sourceDoc: source, plan: plan, response: response,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "typed Rich Text")

	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationTransportShutdownLeavesRunningStateForRedelivery(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_150, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db:        db,
		publisher: stubTranslationJobPublisher{},
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.handleRunningDeliveryFailure(ctx, job, now, context.DeadlineExceeded)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	var stored model.TranslationJob
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, translationJobStatusRunning, stored.Status)
	require.Nil(t, stored.FailureReason)
}

func TestTranslationProviderTimeoutDeletesJobAndArtifacts(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_160, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "provider-timeout", uuid.NewString())
	manager := &TranslationJobManager{
		db:        db,
		publisher: stubTranslationJobPublisher{},
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
	}

	err := manager.handleRunningDeliveryFailure(
		context.Background(),
		job,
		now,
		context.DeadlineExceeded,
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "translation_generation_failed", class)

	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationRedeliveryResumesRunningJob(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db:  db,
		now: func() time.Time { return time.Unix(1_700_000_200, 0).UTC() },
	}

	resumed, err := manager.resumeTranslationJob(context.Background(), job.ID, namedStubTranslationGenerator{
		providerName: "provider",
		modelName:    "model",
	})
	require.NoError(t, err)
	require.True(t, resumed)

	var stored model.TranslationJob
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, translationJobStatusRunning, stored.Status)
}

func TestTranslationProcessDeliveryResumesRunningJobFromPersistedState(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	manager := &TranslationJobManager{
		db:                db,
		generatorResolver: unavailableTranslationGeneratorResolver{},
		publisher:         stubTranslationJobPublisher{},
		now:               func() time.Time { return time.Unix(1_700_000_300, 0).UTC() },
		metrics:           newTranslationMetrics(),
	}

	err := manager.ProcessDelivery(context.Background(), job.ID)
	require.ErrorIs(t, err, errTranslationProviderUnavailable)
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestPrepareTranslationDeliveryUsesPersistedRequestArtifact(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", uuid.NewString())
	source := &translation.SourceDocument{ContentBlockDocument: testPostVersionLocalizedDocument()}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, source)
	require.NoError(t, err)
	request, err := buildTranslationProviderRequest(job, plan)
	require.NoError(t, err)
	artifact, err := translation.BuildRequestArtifact(request, plan)
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		`UPDATE translation_job SET request_xliff = ?, request_manifest = ?, request_artifact_digest = ? WHERE id = ?`,
		artifact.XLIFF, artifact.Manifest, artifact.Digest, job.ID,
	).Error)
	job.RequestArtifactDigest = artifact.Digest
	manager := &TranslationJobManager{
		db: db, publisher: stubTranslationJobPublisher{},
		now:     func() time.Time { return time.Unix(1_700_000_300, 0).UTC() },
		metrics: newTranslationMetrics(),
	}
	execution := &translationDeliveryExecution{job: job, startedAt: manager.now()}

	proceed, err := manager.prepareTranslationDelivery(context.Background(), execution)
	require.NoError(t, err)
	require.True(t, proceed)
	require.Nil(t, execution.sourceDoc)
	require.Equal(t, plan, execution.plan)
	require.Equal(t, request.RequestID, execution.request.RequestID)
	require.Equal(t, request.Document, execution.request.Document)
}

type unavailableTranslationGeneratorResolver struct{}

func (unavailableTranslationGeneratorResolver) Resolve(context.Context) (translation.Generator, error) {
	return nil, errTranslationProviderUnavailable
}

func (unavailableTranslationGeneratorResolver) ResolveAll(context.Context) ([]translation.Generator, error) {
	return nil, errTranslationProviderUnavailable
}

func (unavailableTranslationGeneratorResolver) HasAvailableProvider(context.Context) (bool, error) {
	return false, nil
}

func newTranslationRetryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:translation-retry-"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE translation_job (
		id TEXT PRIMARY KEY,
		entity_type TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		target_locale TEXT NOT NULL,
		source_locale TEXT NOT NULL,
		request_artifact_digest TEXT NOT NULL,
		operation_id TEXT NOT NULL,
		status TEXT NOT NULL,
		requested_by_member_id TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		provider_document_id TEXT,
		provider_document_key TEXT,
		provider_document_submitted_at DATETIME,
		request_xliff BLOB NOT NULL,
		request_manifest BLOB NOT NULL,
		requested_at DATETIME NOT NULL,
		started_at DATETIME,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error)
	return db
}

func seedTranslationRetryTestJob(
	t *testing.T,
	db *gorm.DB,
	status string,
	operationPrefix string,
	entityID string,
) *model.TranslationJob {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	job := &model.TranslationJob{
		ID:                    uuid.NewString(),
		EntityType:            "post",
		EntityID:              entityID,
		TargetLocale:          "ko",
		SourceLocale:          "en",
		RequestArtifactDigest: "digest-" + uuid.NewString(),
		OperationID:           operationPrefix + ":" + uuid.NewString(),
		Status:                status,
		RequestedByMemberID:   uuid.NewString(),
		RequestedAt:           now,
		CreatedAt:             now,
		UpdatedAt:             now,
		RequestXLIFF:          []byte("request"),
		RequestManifest:       []byte("{}"),
	}
	require.NoError(t, db.Create(job).Error)
	return job
}

func translationTestPointerValue[T interface{}](t *testing.T, value *T) T {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
