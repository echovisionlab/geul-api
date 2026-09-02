package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type scriptedDocumentGenerator struct {
	provider          string
	model             string
	session           *scriptedDocumentSession
	documentStarts    int
	synchronousStarts int
}

type scriptedDocumentSession struct {
	handles          []translation.ProviderDocumentHandle
	uploadErrors     []error
	checks           []translation.ProviderDocumentCheck
	checkErrors      []error
	downloadResponse *translation.ProviderResponse
	downloadErrors   []error
	uploadCalls      int
	checkCalls       int
	downloadCalls    int
	closeCalls       int
	uploadRequests   []translation.ProviderRequest
	onCheck          func(translation.ProviderDocumentHandle)
}

func (g *scriptedDocumentGenerator) StartSession(
	context.Context,
	translation.ProviderRequest,
) (translation.GeneratorSession, error) {
	g.synchronousStarts++
	return nil, fmt.Errorf("synchronous document generation must not be used")
}

func (g *scriptedDocumentGenerator) Translate(
	context.Context,
	translation.ProviderRequest,
) (*translation.ProviderResponse, error) {
	return nil, fmt.Errorf("synchronous document generation must not be used")
}

func (g *scriptedDocumentGenerator) ProviderName() string { return g.provider }
func (g *scriptedDocumentGenerator) ModelName() string    { return g.model }

func (g *scriptedDocumentGenerator) StartDocumentSession(
	context.Context,
	translation.ProviderRequest,
) (translation.ResumableDocumentSession, error) {
	g.documentStarts++
	return g.session, nil
}

func (s *scriptedDocumentSession) UploadDocument(
	_ context.Context,
	req translation.ProviderRequest,
) (translation.ProviderDocumentHandle, error) {
	index := s.uploadCalls
	s.uploadCalls++
	s.uploadRequests = append(s.uploadRequests, req)
	if index < len(s.uploadErrors) && s.uploadErrors[index] != nil {
		return translation.ProviderDocumentHandle{}, s.uploadErrors[index]
	}
	if index >= len(s.handles) {
		return translation.ProviderDocumentHandle{}, fmt.Errorf("missing scripted document handle")
	}
	return s.handles[index], nil
}

func (s *scriptedDocumentSession) CheckDocument(
	_ context.Context,
	handle translation.ProviderDocumentHandle,
) (translation.ProviderDocumentCheck, error) {
	if s.onCheck != nil {
		s.onCheck(handle)
	}
	index := s.checkCalls
	s.checkCalls++
	if index < len(s.checkErrors) && s.checkErrors[index] != nil {
		return translation.ProviderDocumentCheck{}, s.checkErrors[index]
	}
	if index >= len(s.checks) {
		return translation.ProviderDocumentCheck{}, fmt.Errorf("missing scripted document check")
	}
	return s.checks[index], nil
}

func (s *scriptedDocumentSession) DownloadDocument(
	context.Context,
	translation.ProviderRequest,
	translation.ProviderDocumentHandle,
) (*translation.ProviderResponse, error) {
	index := s.downloadCalls
	s.downloadCalls++
	if index < len(s.downloadErrors) && s.downloadErrors[index] != nil {
		return nil, s.downloadErrors[index]
	}
	if s.downloadResponse == nil {
		return nil, fmt.Errorf("missing scripted document response")
	}
	return s.downloadResponse, nil
}

func (s *scriptedDocumentSession) Close(context.Context) error {
	s.closeCalls++
	return nil
}

func TestResumableDocumentUploadPersistsPollsAndDownloadsWholeXLIFF(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	handle := providerDocumentHandleForServiceTest(t, "document-1", "secret-key-1")
	session := &scriptedDocumentSession{
		handles: []translation.ProviderDocumentHandle{handle},
		checks: []translation.ProviderDocumentCheck{
			{State: translation.ProviderDocumentPending},
			{State: translation.ProviderDocumentComplete},
		},
		downloadResponse: documentTranslationResponse(request),
		onCheck: func(polledHandle translation.ProviderDocumentHandle) {
			stored := loadDocumentTranslationTestJob(t, manager, job.ID)
			require.Equal(t, handle.DocumentID(), polledHandle.DocumentID())
			require.Equal(t, handle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
			require.NotNil(t, stored.ProviderDocumentSubmittedAt)
		},
	}
	generator := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(), job, request, generator,
	)
	require.NoError(t, err)
	require.Len(t, translation.XLIFFTargets(response.Document), 2)
	require.Equal(t, 1, session.uploadCalls)
	require.Equal(t, 2, session.checkCalls)
	require.Equal(t, 1, session.downloadCalls)
	require.Equal(t, 1, session.closeCalls)
	require.Len(t, session.uploadRequests, 1)
	require.Len(t, session.uploadRequests[0].Document.File.Groups, 2)
	require.Zero(t, generator.synchronousStarts)

	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Equal(t, handle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
	require.Equal(t, handle.DocumentKey(), requireDocumentStringValue(t, stored.ProviderDocumentKey))
	require.NotNil(t, stored.ProviderDocumentSubmittedAt)
}

func TestResumableDocumentRedeliveryResumesPersistedHandleWithoutUpload(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	handle := providerDocumentHandleForServiceTest(t, "document-resume", "secret-key-resume")
	require.NoError(t, manager.persistTranslationProviderDocumentHandle(
		context.Background(), job.ID, "deepl", "quality_optimized", handle, manager.now().UTC(),
	))
	job = loadDocumentTranslationTestJob(t, manager, job.ID)
	session := &scriptedDocumentSession{
		checks:           []translation.ProviderDocumentCheck{{State: translation.ProviderDocumentComplete}},
		downloadResponse: documentTranslationResponse(request),
	}
	generator := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}

	_, err := manager.generateValidatedTranslationWithGenerator(context.Background(), job, request, generator)
	require.NoError(t, err)
	require.Zero(t, session.uploadCalls)
	require.Equal(t, 1, session.checkCalls)
	require.Equal(t, 1, session.downloadCalls)
}

func TestExactTranslationProviderDocumentGeneratorIgnoresConfigurationOrder(t *testing.T) {
	handle := providerDocumentHandleForServiceTest(t, "document-exact", "secret-key-exact")
	submittedAt := time.Unix(1_700_000_400, 0).UTC()
	provider := "provider-b"
	modelName := "model-b"
	job := &model.TranslationJob{
		Provider:                    &provider,
		Model:                       &modelName,
		ProviderDocumentID:          translationProviderDocumentStringPointer(handle.DocumentID()),
		ProviderDocumentKey:         translationProviderDocumentStringPointer(handle.DocumentKey()),
		ProviderDocumentSubmittedAt: &submittedAt,
	}
	first := namedStubTranslationGenerator{providerName: "provider-a", modelName: "model-a"}
	exact := &scriptedDocumentGenerator{provider: provider, model: modelName, session: &scriptedDocumentSession{}}

	selected, err := exactTranslationProviderDocumentGenerator(job, []translation.Generator{first, exact})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	require.Same(t, exact, selected[0])

	_, err = exactTranslationProviderDocumentGenerator(job, []translation.Generator{first})
	require.ErrorIs(t, err, errTranslationProviderUnavailable)

	ambiguous := &scriptedDocumentGenerator{provider: provider, model: modelName, session: &scriptedDocumentSession{}}
	_, err = exactTranslationProviderDocumentGenerator(job, []translation.Generator{exact, ambiguous})
	require.ErrorIs(t, err, errTranslationProviderUnavailable)
}

func TestResumableDocumentNotFoundFailsWithoutAutomaticReupload(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	firstHandle := providerDocumentHandleForServiceTest(t, "document-expired", "secret-key-expired")
	session := &scriptedDocumentSession{
		handles: []translation.ProviderDocumentHandle{firstHandle},
		checks: []translation.ProviderDocumentCheck{
			{State: translation.ProviderDocumentNotFound},
		},
	}
	generator := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}

	_, err := manager.generateValidatedTranslationWithGenerator(context.Background(), job, request, generator)
	require.ErrorIs(t, err, translation.ErrProviderDocumentNotFound)
	require.Equal(t, 1, session.uploadCalls)
	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Equal(t, firstHandle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
	require.Equal(t, "deepl", requireDocumentStringValue(t, stored.Provider))
	require.Equal(t, "quality_optimized", requireDocumentStringValue(t, stored.Model))
}

func TestExplicitRetryReplacesPersistedDocumentOnlyAfterTypedNotFound(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	expiredHandle := providerDocumentHandleForServiceTest(t, "document-expired", "secret-key-expired")
	replacementHandle := providerDocumentHandleForServiceTest(t, "document-replacement", "secret-key-replacement")
	require.NoError(t, manager.persistTranslationProviderDocumentHandle(
		context.Background(), job.ID, "deepl", "quality_optimized", expiredHandle, manager.now().UTC(),
	))
	job = loadDocumentTranslationTestJob(t, manager, job.ID)
	session := &scriptedDocumentSession{
		handles: []translation.ProviderDocumentHandle{replacementHandle},
		checks: []translation.ProviderDocumentCheck{
			{State: translation.ProviderDocumentNotFound},
			{State: translation.ProviderDocumentComplete},
		},
		downloadResponse: documentTranslationResponse(request),
	}
	generator := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(), job, request, generator, true,
	)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, 1, session.uploadCalls)
	require.Equal(t, 2, session.checkCalls)
	require.Equal(t, 1, session.downloadCalls)
	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Equal(t, replacementHandle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
}

func TestQueuedProviderDocumentReplacementRequiresExplicitRetryMarker(t *testing.T) {
	requestedBy := uuid.NewString()
	provider := "deepl"
	modelName := "quality_optimized"
	documentID := "document-id"
	documentKey := "document-key"
	submittedAt := time.Now().UTC()
	job := &model.TranslationJob{
		Status:                      translationJobStatusQueued,
		Provider:                    &provider,
		Model:                       &modelName,
		ProviderDocumentID:          &documentID,
		ProviderDocumentKey:         &documentKey,
		ProviderDocumentSubmittedAt: &submittedAt,
	}
	require.False(t, queuedTranslationJobHasProviderDocument(job))
	job.RequestedByMemberID = requestedBy
	require.True(t, queuedTranslationJobHasProviderDocument(job))
	job.Status = translationJobStatusRunning
	require.False(t, queuedTranslationJobHasProviderDocument(job))
}

func TestResumableDocumentErrorRetainsHandleAndDoesNotFallback(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	handle := providerDocumentHandleForServiceTest(t, "document-error", "secret-key-error")
	session := &scriptedDocumentSession{
		handles: []translation.ProviderDocumentHandle{handle},
		checks: []translation.ProviderDocumentCheck{{
			State: translation.ProviderDocumentError, ErrorMessage: "provider rejected " + handle.DocumentKey(),
		}},
	}
	primary := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}
	fallbackSession := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{documentTranslationResponse(request)},
	}
	fallback := &scriptedTranslationGenerator{session: fallbackSession}

	_, used, err := manager.generateValidatedTranslation(
		context.Background(), job, request, []translation.Generator{primary, fallback},
	)
	require.ErrorIs(t, err, errTranslationProviderDocumentRejected)
	require.NotContains(t, err.Error(), handle.DocumentKey())
	require.Same(t, primary, used)
	require.Zero(t, fallbackSession.initialCalls)
	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Equal(t, handle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
}

func TestResumableDocumentUploadFailureDoesNotFallback(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "unit-1", Source: "Hello"},
	)
	primarySession := &scriptedDocumentSession{
		uploadErrors: []error{fmt.Errorf("document upload unavailable")},
	}
	primary := &scriptedDocumentGenerator{
		provider: "deepl", model: "quality_optimized", session: primarySession,
	}
	fallbackSession := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "unit-1", TranslatedText: "Bonjour"},
		)},
	}
	fallback := &scriptedTranslationGenerator{session: fallbackSession}

	response, used, err := manager.generateValidatedTranslation(
		context.Background(), job, request, []translation.Generator{primary, fallback},
	)
	require.Error(t, err)
	require.Nil(t, response)
	require.Same(t, primary, used)
	require.Equal(t, 1, primarySession.uploadCalls)
	require.Zero(t, fallbackSession.initialCalls)
	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Nil(t, stored.ProviderDocumentID)
	require.Equal(t, primary.ProviderName(), requireDocumentStringValue(t, stored.Provider))
}

func TestResumableDocumentValidatesRawResponseWithoutRepair(t *testing.T) {
	manager, job := runningDocumentTranslationTestManager(t, "deepl", "quality_optimized")
	request := multiGroupDocumentTranslationRequest()
	handle := providerDocumentHandleForServiceTest(t, "document-invalid", "secret-key-invalid")
	invalid := documentTranslationResponse(request)
	invalid.Document.File.ID = "wrong-source-identity"
	session := &scriptedDocumentSession{
		handles:          []translation.ProviderDocumentHandle{handle},
		checks:           []translation.ProviderDocumentCheck{{State: translation.ProviderDocumentComplete}},
		downloadResponse: invalid,
	}
	generator := &scriptedDocumentGenerator{provider: "deepl", model: "quality_optimized", session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(), job, request, generator,
	)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.Zero(t, generator.synchronousStarts)
	require.Equal(t, 1, session.uploadCalls)
	require.Equal(t, 1, session.downloadCalls)
	stored := loadDocumentTranslationTestJob(t, manager, job.ID)
	require.Equal(t, handle.DocumentID(), requireDocumentStringValue(t, stored.ProviderDocumentID))
}

func runningDocumentTranslationTestManager(
	t *testing.T,
	provider string,
	modelName string,
) (*TranslationJobManager, *model.TranslationJob) {
	t.Helper()
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_500, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "operation", "entity-document")
	manager := &TranslationJobManager{db: db, now: func() time.Time { return now }}
	require.NoError(t, manager.updateRunningJobProvider(
		context.Background(), job.ID,
		namedStubTranslationGenerator{providerName: provider, modelName: modelName},
	))
	return manager, loadDocumentTranslationTestJob(t, manager, job.ID)
}

func loadDocumentTranslationTestJob(
	t *testing.T,
	manager *TranslationJobManager,
	jobID string,
) *model.TranslationJob {
	t.Helper()
	job, err := manager.loadTranslationJob(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, job)
	return job
}

func multiGroupDocumentTranslationRequest() translation.ProviderRequest {
	profile := translation.GenerationProfile{
		QualityTier:  translation.QualityTierStandard,
		SourceLocale: "en",
		TargetLocale: "fr",
		MIMEType:     "text/plain",
	}
	return translationProviderRequestForTest(
		profile,
		translation.XLIFFGroup{
			ID: "group-1", SequenceIndex: 1, SequenceTotal: 2,
			TranslationUnit: []translation.XLIFFUnit{{ID: "unit-1", Source: "Hello"}},
		},
		translation.XLIFFGroup{
			ID: "group-2", SequenceIndex: 2, SequenceTotal: 2,
			TranslationUnit: []translation.XLIFFUnit{{ID: "unit-2", Source: "World"}},
		},
	)
}

func documentTranslationResponse(request translation.ProviderRequest) *translation.ProviderResponse {
	response := translationProviderResponseForTest(
		request,
		translation.UnitResult{UnitID: "unit-1", TranslatedText: "Bonjour"},
		translation.UnitResult{UnitID: "unit-2", TranslatedText: "Monde"},
	)
	return &response
}

func providerDocumentHandleForServiceTest(
	t *testing.T,
	documentID string,
	documentKey string,
) translation.ProviderDocumentHandle {
	t.Helper()
	handle, err := translation.NewProviderDocumentHandle(documentID, documentKey)
	require.NoError(t, err)
	return handle
}

func requireDocumentStringValue(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

var _ translation.ResumableDocumentGenerator = (*scriptedDocumentGenerator)(nil)
var _ translation.ResumableDocumentSession = (*scriptedDocumentSession)(nil)
