package application

import (
	"context"
	"fmt"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type stubTranslationGeneratorSession struct{}

type scriptedTranslationGenerator struct {
	session      *scriptedTranslationGeneratorSession
	providerName string
}

type namedStubTranslationGenerator struct {
	providerName string
	modelName    string
}

type stubTranslationJobPublisher struct {
	noopAsyncPublisher
}

type scriptedTranslationGeneratorSession struct {
	initialResponses []*translation.ProviderResponse
	initialErrors    []error
	initialCalls     int
	requests         []translation.ProviderRequest
	closed           bool
}

func (stubTranslationGeneratorSession) Translate(
	_ context.Context,
	_ translation.ProviderRequest,
) (*translation.ProviderResponse, error) {
	return &translation.ProviderResponse{}, nil
}

func (stubTranslationGeneratorSession) Close(context.Context) error {
	return nil
}

func (g *scriptedTranslationGenerator) StartSession(
	_ context.Context,
	_ translation.ProviderRequest,
) (translation.GeneratorSession, error) {
	if g.session == nil {
		return nil, fmt.Errorf("missing scripted session")
	}
	return g.session, nil
}

func (g *scriptedTranslationGenerator) Translate(
	ctx context.Context,
	req translation.ProviderRequest,
) (*translation.ProviderResponse, error) {
	session, err := g.StartSession(ctx, req)
	if err != nil {
		return nil, err
	}
	return session.Translate(ctx, req)
}

func (g *scriptedTranslationGenerator) ProviderName() string {
	if g.providerName != "" {
		return g.providerName
	}
	return "scripted-provider"
}

func (g *scriptedTranslationGenerator) ModelName() string {
	return "scripted-model"
}

func (g namedStubTranslationGenerator) StartSession(
	_ context.Context,
	_ translation.ProviderRequest,
) (translation.GeneratorSession, error) {
	return stubTranslationGeneratorSession{}, nil
}

func (g namedStubTranslationGenerator) Translate(
	_ context.Context,
	_ translation.ProviderRequest,
) (*translation.ProviderResponse, error) {
	return &translation.ProviderResponse{}, nil
}

func (g namedStubTranslationGenerator) ProviderName() string {
	return g.providerName
}

func (g namedStubTranslationGenerator) ModelName() string {
	return g.modelName
}

func (stubTranslationJobPublisher) PublishTranslationGenerate(
	context.Context,
	*managev1.TranslationGenerateEvent,
) error {
	return nil
}

func (stubTranslationJobPublisher) PublishTranslationLifecycle(
	context.Context,
	*managev1.TranslationLifecycleEvent,
) error {
	return nil
}

func (stubTranslationJobPublisher) PublishContentUpdatedWithExecutor(
	context.Context,
	eventpkg.DBTX,
	*managev1.ContentUpdatedEvent,
) error {
	return nil
}

func (s *scriptedTranslationGeneratorSession) Translate(
	_ context.Context,
	req translation.ProviderRequest,
) (*translation.ProviderResponse, error) {
	index := s.initialCalls
	s.initialCalls++
	s.requests = append(s.requests, req)
	if index < len(s.initialErrors) && s.initialErrors[index] != nil {
		return nil, s.initialErrors[index]
	}
	if index < len(s.initialResponses) && s.initialResponses[index] != nil {
		return s.initialResponses[index], nil
	}
	return &translation.ProviderResponse{}, nil
}

func TestGenerateValidatedTranslationSubmitsWholeMultiGroupXLIFFOnce(t *testing.T) {
	t.Parallel()

	request := multiGroupDocumentTranslationRequest()
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{documentTranslationResponse(request)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(),
		&model.TranslationJob{},
		request,
		generator,
	)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, 1, session.initialCalls)
	require.Len(t, session.requests, 1)
	require.Len(t, session.requests[0].Document.File.Groups, len(request.Document.File.Groups))
	require.Len(t, translation.XLIFFTargets(response.Document), 2)
}

func (s *scriptedTranslationGeneratorSession) Close(context.Context) error {
	s.closed = true
	return nil
}

func translationJobProviderRequest(sourceUnits ...translation.XLIFFUnit) translation.ProviderRequest {
	profile := translation.GenerationProfile{
		QualityTier:  translation.QualityTierStandard,
		SourceLocale: "en",
		TargetLocale: "fr",
		MIMEType:     "text/plain",
	}
	return translation.ProviderRequest{
		RequestID:   "req-1",
		OperationID: "operation-1",
		Profile:     profile,
		Document: translation.XLIFFDocument{
			Version:      translation.XLIFFVersion,
			SourceLocale: profile.SourceLocale,
			TargetLocale: profile.TargetLocale,
			File: translation.XLIFFFile{
				ID: "post:1",
				Groups: []translation.XLIFFGroup{{
					ID:              "block:1",
					SequenceIndex:   1,
					SequenceTotal:   1,
					TranslationUnit: sourceUnits,
				}},
			},
		},
	}
}

func translationJobProviderResponse(
	request translation.ProviderRequest,
	targets ...translation.UnitResult,
) *translation.ProviderResponse {
	type unitLocation struct {
		groupIndex int
		unit       translation.XLIFFUnit
	}
	knownUnits := make(map[string]unitLocation)
	for groupIndex, group := range request.Document.File.Groups {
		for _, unit := range group.TranslationUnit {
			knownUnits[unit.ID] = unitLocation{groupIndex: groupIndex, unit: unit}
		}
	}
	document := request.Document
	document.File.Groups = make([]translation.XLIFFGroup, len(request.Document.File.Groups))
	for groupIndex, group := range request.Document.File.Groups {
		group.TranslationUnit = nil
		document.File.Groups[groupIndex] = group
	}
	for _, target := range targets {
		location, known := knownUnits[target.UnitID]
		unit := location.unit
		unit.ID = target.UnitID
		translated := target.TranslatedText
		unit.Target = &translated
		unit.TargetInline = append([]translation.XLIFFInline(nil), target.TargetInline...)
		if known {
			document.File.Groups[location.groupIndex].TranslationUnit = append(document.File.Groups[location.groupIndex].TranslationUnit, unit)
			continue
		}
		if len(document.File.Groups) == len(request.Document.File.Groups) {
			document.File.Groups = append(document.File.Groups, translation.XLIFFGroup{ID: "provider:unknown"})
		}
		lastGroup := len(document.File.Groups) - 1
		document.File.Groups[lastGroup].TranslationUnit = append(document.File.Groups[lastGroup].TranslationUnit, unit)
	}
	raw, err := translation.NewProviderRawResponse("application/json", []byte(`{"provider":"test"}`))
	if err != nil {
		panic(err)
	}
	return &translation.ProviderResponse{
		Document: document,
		Raw:      raw,
	}
}

func TestTranslationJobManagerConstructorsConfigureResolvers(t *testing.T) {
	t.Parallel()

	db := &gorm.DB{}
	publisher := stubTranslationJobPublisher{}

	planner := &og.Planner{}
	refresher := &og.Refresher{}
	manager, err := NewTranslationJobManager(db, publisher, planner, refresher)
	require.NoError(t, err)
	assert.Same(t, db, manager.db)
	assert.NotNil(t, manager.generatorResolver)
}

func TestTranslationJobManagerRequiresResolver(t *testing.T) {
	t.Parallel()

	_, err := (&TranslationJobManager{}).resolveGenerators(context.Background())
	require.ErrorIs(t, err, errTranslationProviderUnavailable)
}

func TestGenerateValidatedTranslationRejectsMissingUnitsWithoutRepair(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Hello"},
		translation.XLIFFUnit{ID: "block:1:text:1", Source: "world"},
	)
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Bonjour"},
		)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}
	job := &model.TranslationJob{}

	response, err := manager.generateValidatedTranslationWithGenerator(context.Background(), job, request, generator)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.Equal(t, 1, session.initialCalls)
	require.True(t, session.closed)
}

func TestGenerateValidatedTranslationDoesNotFallbackToNextProvider(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Hello"},
	)
	primarySession := &scriptedTranslationGeneratorSession{
		initialErrors: []error{fmt.Errorf("primary failed")},
	}
	fallbackSession := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Bonjour"},
		)},
	}
	manager := &TranslationJobManager{}
	job := &model.TranslationJob{ID: "job-1"}
	primary := &scriptedTranslationGenerator{session: primarySession}
	fallback := &scriptedTranslationGenerator{session: fallbackSession}

	response, usedGenerator, err := manager.generateValidatedTranslation(
		context.Background(),
		job,
		request,
		[]translation.Generator{primary, fallback},
	)
	require.Error(t, err)
	require.Nil(t, response)
	require.Same(t, primary, usedGenerator)
	require.Equal(t, 1, primarySession.initialCalls)
	require.Zero(t, fallbackSession.initialCalls)
	require.True(t, primarySession.closed)
	require.False(t, fallbackSession.closed)
}

func TestGenerateValidatedTranslationRejectsUnknownUnitsWithoutRepair(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Hello"},
	)
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Bonjour"},
			translation.UnitResult{UnitID: "unknown:text:0", TranslatedText: "[fr] Inconnu"},
		)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(),
		&model.TranslationJob{},
		request,
		generator,
	)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.True(t, session.closed)
}

func TestGenerateValidatedTranslationRejectsDuplicateUnitsWithoutRepair(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Hello"},
	)
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Bonjour"},
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Salut"},
		)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}

	response, err := manager.generateValidatedTranslationWithGenerator(
		context.Background(),
		&model.TranslationJob{},
		request,
		generator,
	)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.True(t, session.closed)
}

func TestGenerateValidatedTranslationRejectsPlaceholderMismatchWithoutRepair(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Hello {{name}}"},
	)
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Bonjour"},
		)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}
	job := &model.TranslationJob{}

	response, err := manager.generateValidatedTranslationWithGenerator(context.Background(), job, request, generator)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.Equal(t, 1, session.initialCalls)
	require.True(t, session.closed)
}

func TestGenerateValidatedTranslationRejectsLineBreakMismatchWithoutRepair(t *testing.T) {
	t.Parallel()

	request := translationJobProviderRequest(
		translation.XLIFFUnit{ID: "block:1:text:0", Source: "Line one\nLine two"},
	)
	session := &scriptedTranslationGeneratorSession{
		initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(
			request,
			translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[fr] Ligne un Ligne deux"},
		)},
	}
	manager := &TranslationJobManager{}
	generator := &scriptedTranslationGenerator{session: session}
	job := &model.TranslationJob{}

	response, err := manager.generateValidatedTranslationWithGenerator(context.Background(), job, request, generator)
	require.Nil(t, response)
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	require.Equal(t, 1, session.initialCalls)
	require.True(t, session.closed)
}
