package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// env is the Translation use cases and job runner over one fake Store.
type env struct {
	store     *translationtest.Store
	units     *mutationtest.UnitOfWork[application.Store]
	jobUnits  *mutationtest.UnitOfWork[application.JobStore]
	checker   *authztest.Checker
	fence     *identitystatetest.Fence
	audit     *audittest.Recorder
	lifecycle *recordingLifecycle
	files     *recordingXLIFFFiles
	service   *application.Service
	runner    *application.TranslationJobManager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, units := translationtest.New()
	e := &env{
		store: store, units: units, jobUnits: store.JobUnits(), checker: authztest.AllowAll(),
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, lifecycle: &recordingLifecycle{},
		files: &recordingXLIFFFiles{},
	}
	service, err := application.NewService(application.Deps{
		Units: units, SpiceDB: e.checker, Fence: e.fence, Audit: e.audit,
		Plans: translationtest.Plans{}, XLIFFFiles: e.files,
	})
	require.NoError(t, err)
	runner, err := application.NewTranslationJobManager(application.JobManagerDeps{
		Units: e.jobUnits, SpiceDB: e.checker, Fence: e.fence, Audit: e.audit,
		Plans: translationtest.Plans{}, Lifecycle: e.lifecycle,
	})
	require.NoError(t, err)
	e.service, e.runner = service, runner
	return e
}

// principal is an authenticated, onboarded Member with a request context,
// as the Connect and MCP gateways hand it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	require.NoError(t, err)
	identityID, memberID := uuid.NewString(), uuid.NewString()
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func codeOf(err error) connect.Code {
	return errs.Wrap(err).Code()
}

// seedSeries stores a Post Series with an English title and summary.
func (e *env) seedSeries(t *testing.T) *translationtest.Entity {
	t.Helper()
	return e.store.SeedEntity(translationtest.Entity{
		EntityType: "series", EntityID: uuid.NewString(), SourceLocale: "en",
		Source: &translation.SourceDocument{Title: "Night walks", Summary: stringPointer("Field recordings")},
	})
}

// seedProvider stores an active DeepL provider, which makes generation
// available.
func (e *env) seedProvider(t *testing.T) {
	t.Helper()
	provider := application.Provider{Name: "DeepL", Type: application.ProviderTypeDeepL, IsActive: true, Priority: 1}
	require.NoError(t, provider.SetConfig(application.DeepLConfig{APIKey: "key", APIBaseURL: translation.DefaultDeepLAPIBaseURL}))
	e.store.SeedProvider(provider)
}

// seedRunningJob stores a running job whose request artifact the runner
// never reads.
func (e *env) seedRunningJob(t *testing.T, now time.Time) application.Job {
	t.Helper()
	job := translationtest.NewJob(uuid.NewString(), now)
	job.Status = application.JobRunning
	job.StartedAt = &now
	e.store.SeedJob(job)
	return job
}

// queue regenerates the series into locale and returns the queued job.
func (e *env) queue(t *testing.T, p principal, entity *translationtest.Entity, locale string) application.Job {
	t.Helper()
	jobs, err := e.service.RegenerateEntityTranslations(p.ctx, application.RegenerateEntityTranslationsCommand{
		Target: &application.Target{EntityType: entity.EntityType, EntityID: entity.EntityID}, Locales: []string{locale},
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	return jobs[0]
}

func stringPointer(value string) *string { return &value }

type recordingLifecycle struct {
	mu     sync.Mutex
	events []application.LifecycleEvent
	err    error
}

func (r *recordingLifecycle) PublishTranslationLifecycle(_ context.Context, event application.LifecycleEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *recordingLifecycle) Events() []application.LifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]application.LifecycleEvent(nil), r.events...)
}

// recordingXLIFFFiles stores exports as a short-lived download and serves
// one upload.
type recordingXLIFFFiles struct {
	created []application.TranslationXLIFFArtifact
	upload  application.VerifiedTranslationXLIFF
}

func (f *recordingXLIFFFiles) CreateTranslationXLIFF(_ context.Context, artifact application.TranslationXLIFFArtifact) (*commonv1.ExpiringMediaRef, error) {
	f.created = append(f.created, artifact)
	return &commonv1.ExpiringMediaRef{
		FileId: uuid.NewString(), Url: "https://cdn.example/xliff/signed",
		ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
		Purpose:   commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD,
	}, nil
}

func (f *recordingXLIFFFiles) ReadVerifiedTranslationXLIFF(_ context.Context, fileID string, _ int64) (application.VerifiedTranslationXLIFF, error) {
	if f.upload.FileID != fileID {
		return application.VerifiedTranslationXLIFF{}, errors.New("no such upload")
	}
	return f.upload, nil
}

// echoGenerator answers every unit with its source text, which passes the
// response validation unchanged.
type echoGenerator struct {
	provider, model string
	err             error
	// during runs inside the provider call, as a concurrent request would.
	during func()
	calls  int
}

func (g *echoGenerator) ProviderName() string { return g.provider }
func (g *echoGenerator) ModelName() string    { return g.model }

func (g *echoGenerator) StartSession(context.Context, translation.ProviderRequest) (translation.GeneratorSession, error) {
	return echoSession{generator: g}, nil
}

func (g *echoGenerator) Translate(ctx context.Context, request translation.ProviderRequest) (*translation.ProviderResponse, error) {
	return echoSession{generator: g}.Translate(ctx, request)
}

type echoSession struct{ generator *echoGenerator }

func (s echoSession) Translate(_ context.Context, request translation.ProviderRequest) (*translation.ProviderResponse, error) {
	s.generator.calls++
	if s.generator.during != nil {
		s.generator.during()
	}
	if s.generator.err != nil {
		return nil, s.generator.err
	}
	targets := map[string]translation.UnitResult{}
	for _, group := range request.Document.File.Groups {
		for _, unit := range group.TranslationUnit {
			targets[unit.ID] = translation.UnitResult{
				UnitID: unit.ID, TranslatedText: unit.Source, TargetInline: unit.SourceInline,
				OriginalData: unit.OriginalData,
			}
		}
	}
	response := translationProviderResponseForTest(request)
	response.Document = translation.XLIFFWithTargets(request.Document, targets)
	return &response, nil
}

func (echoSession) Close(context.Context) error { return nil }

type namedStubTranslationGenerator struct {
	providerName string
	modelName    string
}

func (g namedStubTranslationGenerator) StartSession(context.Context, translation.ProviderRequest) (translation.GeneratorSession, error) {
	return nil, fmt.Errorf("stub generator %s/%s does not translate", g.providerName, g.modelName)
}

func (g namedStubTranslationGenerator) Translate(context.Context, translation.ProviderRequest) (*translation.ProviderResponse, error) {
	return nil, fmt.Errorf("stub generator %s/%s does not translate", g.providerName, g.modelName)
}

func (g namedStubTranslationGenerator) ProviderName() string { return g.providerName }
func (g namedStubTranslationGenerator) ModelName() string    { return g.modelName }

type scriptedTranslationGenerator struct {
	session *scriptedTranslationGeneratorSession
}

func (g *scriptedTranslationGenerator) StartSession(context.Context, translation.ProviderRequest) (translation.GeneratorSession, error) {
	return g.session, nil
}

func (g *scriptedTranslationGenerator) Translate(ctx context.Context, request translation.ProviderRequest) (*translation.ProviderResponse, error) {
	return g.session.Translate(ctx, request)
}

func (g *scriptedTranslationGenerator) ProviderName() string { return "scripted" }
func (g *scriptedTranslationGenerator) ModelName() string    { return "scripted" }

type scriptedTranslationGeneratorSession struct {
	initialResponses []*translation.ProviderResponse
	initialCalls     int
}

func (s *scriptedTranslationGeneratorSession) Translate(context.Context, translation.ProviderRequest) (*translation.ProviderResponse, error) {
	index := s.initialCalls
	s.initialCalls++
	if index >= len(s.initialResponses) {
		return nil, fmt.Errorf("missing scripted response")
	}
	return s.initialResponses[index], nil
}

func (s *scriptedTranslationGeneratorSession) Close(context.Context) error { return nil }

func translationProviderRequestForTest(
	profile translation.GenerationProfile,
	groups ...translation.XLIFFGroup,
) translation.ProviderRequest {
	return translation.ProviderRequest{
		RequestID: "job-test", OperationID: "test:1", Profile: profile,
		Document: translation.XLIFFDocument{
			Version:      translation.XLIFFVersion,
			SourceLocale: profile.SourceLocale,
			TargetLocale: profile.TargetLocale,
			File:         translation.XLIFFFile{ID: "test:1", Groups: groups},
		},
	}
}

func translationProviderResponseForTest(
	request translation.ProviderRequest,
	targets ...translation.UnitResult,
) translation.ProviderResponse {
	byID := make(map[string]translation.UnitResult, len(targets))
	for _, target := range targets {
		byID[target.UnitID] = target
	}
	raw, err := translation.NewProviderRawResponse("application/json", []byte(`{"provider":"test"}`))
	if err != nil {
		panic(err)
	}
	return translation.ProviderResponse{
		Document: translation.XLIFFWithTargets(request.Document, byID),
		Raw:      raw,
	}
}

func translationJobProviderRequest(sourceUnits ...translation.XLIFFUnit) translation.ProviderRequest {
	return translationProviderRequestForTest(translation.GenerationProfile{
		QualityTier: translation.QualityTierStandard, SourceLocale: "en", TargetLocale: "fr", MIMEType: "text/plain",
	}, translation.XLIFFGroup{ID: "block:1", SequenceIndex: 1, SequenceTotal: 1, TranslationUnit: sourceUnits})
}

func translationJobProviderResponse(request translation.ProviderRequest, targets ...translation.UnitResult) *translation.ProviderResponse {
	response := translationProviderResponseForTest(request, targets...)
	return &response
}
