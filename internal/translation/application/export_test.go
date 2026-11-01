package application

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// SetGeneratorsForTest makes the runner use generators in place of the
// stored providers, as the resolver would build them; err answers the
// resolution instead when set.
func (m *TranslationJobManager) SetGeneratorsForTest(err error, generators ...translation.Generator) {
	m.generatorResolver = staticGeneratorResolver{generators: generators, err: err}
}

type staticGeneratorResolver struct {
	generators []translation.Generator
	err        error
}

func (r staticGeneratorResolver) Resolve(ctx context.Context) (translation.Generator, error) {
	generators, err := r.ResolveAll(ctx)
	if err != nil {
		return nil, err
	}
	return generators[0], nil
}

func (r staticGeneratorResolver) ResolveAll(context.Context) ([]translation.Generator, error) {
	if r.err != nil {
		return nil, r.err
	}
	if len(r.generators) == 0 {
		return nil, errTranslationProviderUnavailable
	}
	return r.generators, nil
}

func (r staticGeneratorResolver) HasAvailableProvider(ctx context.Context) (bool, error) {
	generators, err := r.ResolveAll(ctx)
	return len(generators) != 0, err
}

// ErrProviderUnavailableForTest is the runner's answer to having no usable
// provider.
var ErrProviderUnavailableForTest = errTranslationProviderUnavailable

// The runner's provider steps, which the resumable-document tests drive
// directly.
func (m *TranslationJobManager) GenerateForTest(ctx context.Context, job *Job, request translation.ProviderRequest, generator translation.Generator, allowReplacement bool) (*translation.ProviderResponse, error) {
	return m.generateValidatedTranslationWithGenerator(ctx, job, request, generator, allowReplacement)
}

func (m *TranslationJobManager) GenerateWithProvidersForTest(ctx context.Context, job *Job, request translation.ProviderRequest, generators []translation.Generator) (*translation.ProviderResponse, translation.Generator, error) {
	return m.generateValidatedTranslation(ctx, job, request, generators)
}

func (m *TranslationJobManager) PersistDocumentHandleForTest(ctx context.Context, jobID, provider, modelName string, handle translation.ProviderDocumentHandle, submittedAt time.Time) error {
	return m.persistTranslationProviderDocumentHandle(ctx, jobID, provider, modelName, handle, submittedAt)
}

func (m *TranslationJobManager) UpdateRunningJobProviderForTest(ctx context.Context, jobID string, generator translation.Generator) error {
	return m.updateRunningJobProvider(ctx, jobID, generator)
}

func (m *TranslationJobManager) ResumeForTest(ctx context.Context, jobID string, generator translation.Generator) (bool, error) {
	return m.resumeTranslationJob(ctx, jobID, generator)
}

func (m *TranslationJobManager) SetClockForTest(now func() time.Time) { m.now = now }

func (s *Service) SetClockForTest(now func() time.Time) { s.now = now }

var (
	ExactProviderDocumentGeneratorForTest = exactTranslationProviderDocumentGenerator
	QueuedJobHasProviderDocumentForTest   = queuedTranslationJobHasProviderDocument

	ErrProviderDocumentRejectedForTest       = errTranslationProviderDocumentRejected
	ErrProviderResponseInvalidForTest        = errTranslationProviderResponseInvalid
	ErrProviderDocumentHandleMismatchForTest = errTranslationProviderDocumentHandleMismatch
	ErrJobNoLongerCurrentForTest             = errTranslationJobNoLongerCurrent
)
