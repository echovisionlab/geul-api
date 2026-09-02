package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type translationDeliveryExecution struct {
	job                             *model.TranslationJob
	startedAt                       time.Time
	allowExpiredDocumentReplacement bool
	sourceDoc                       *translation.SourceDocument
	plan                            *translation.ExtractionPlan
	request                         translation.ProviderRequest
	response                        *translation.ProviderResponse
	generator                       translation.Generator
}

func (m *TranslationJobManager) ProcessDelivery(
	ctx context.Context,
	jobID string,
) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	job, resumeRunning, proceed, err := m.loadEligibleTranslationDelivery(ctx, jobID)
	if err != nil || !proceed {
		return err
	}
	generators, err := m.resolveDeliveryGenerators(ctx, job, resumeRunning)
	if err != nil {
		return err
	}
	allowExpiredDocumentReplacement := !resumeRunning && queuedTranslationJobHasProviderDocument(job)
	job, startedAt, proceed, err := m.claimTranslationDelivery(ctx, job, generators[0], resumeRunning)
	if err != nil || !proceed {
		return err
	}
	execution := &translationDeliveryExecution{
		job:                             job,
		startedAt:                       startedAt,
		allowExpiredDocumentReplacement: allowExpiredDocumentReplacement,
	}
	return m.runClaimedTranslationDelivery(ctx, execution, generators)
}

func (m *TranslationJobManager) loadEligibleTranslationDelivery(
	ctx context.Context,
	jobID string,
) (*model.TranslationJob, bool, bool, error) {
	job, err := m.loadTranslationJob(ctx, jobID)
	if err != nil || job == nil {
		return job, false, false, err
	}
	resumeRunning := job.Status == translationJobStatusRunning
	proceed := job.Status == translationJobStatusQueued || resumeRunning
	return job, resumeRunning, proceed, nil
}

func (m *TranslationJobManager) resolveDeliveryGenerators(
	ctx context.Context,
	job *model.TranslationJob,
	resumeRunning bool,
) ([]translation.Generator, error) {
	generators, err := m.resolveGenerators(ctx)
	if err == nil {
		generators, err = exactTranslationProviderDocumentGenerator(job, generators)
		if err == nil {
			return generators, nil
		}
	}
	if resumeRunning {
		return nil, m.handleRunningDeliveryFailure(
			ctx,
			job,
			translationJobStartedAt(job, m.now().UTC()),
			err,
		)
	}
	return nil, m.handleQueuedDeliveryFailure(ctx, job, err)
}

func exactTranslationProviderDocumentGenerator(
	job *model.TranslationJob,
	generators []translation.Generator,
) ([]translation.Generator, error) {
	if job == nil {
		return generators, nil
	}
	hasID := job.ProviderDocumentID != nil
	hasKey := job.ProviderDocumentKey != nil
	hasSubmittedAt := job.ProviderDocumentSubmittedAt != nil
	if !hasID && !hasKey && !hasSubmittedAt && job.Provider == nil && job.Model == nil {
		return generators, nil
	}
	if job.Provider == nil || job.Model == nil {
		return nil, errTranslationProviderDocumentHandleMismatch
	}
	if !hasID && !hasKey && !hasSubmittedAt {
		return exactTranslationGenerator(job, generators)
	}
	if !hasID || !hasKey || !hasSubmittedAt || job.ProviderDocumentSubmittedAt.IsZero() ||
		job.Provider == nil || job.Model == nil {
		return nil, errTranslationProviderDocumentHandleMismatch
	}
	return exactTranslationGenerator(job, generators)
}

func exactTranslationGenerator(
	job *model.TranslationJob,
	generators []translation.Generator,
) ([]translation.Generator, error) {
	var exact translation.Generator
	for _, generator := range generators {
		if generator.ProviderName() == *job.Provider && generator.ModelName() == *job.Model {
			if job.ProviderDocumentID != nil {
				if _, ok := generator.(translation.ResumableDocumentGenerator); !ok {
					return nil, fmt.Errorf(
						"%w: persisted translation provider %s/%s cannot resume documents",
						errTranslationProviderUnavailable,
						*job.Provider,
						*job.Model,
					)
				}
			}
			if exact != nil {
				return nil, fmt.Errorf(
					"%w: persisted translation provider %s/%s is ambiguous",
					errTranslationProviderUnavailable,
					*job.Provider,
					*job.Model,
				)
			}
			exact = generator
		}
	}
	if exact != nil {
		return []translation.Generator{exact}, nil
	}
	return nil, fmt.Errorf(
		"%w: persisted translation provider %s/%s is unavailable",
		errTranslationProviderUnavailable,
		*job.Provider,
		*job.Model,
	)
}

func (m *TranslationJobManager) claimTranslationDelivery(
	ctx context.Context,
	job *model.TranslationJob,
	generator translation.Generator,
	resumeRunning bool,
) (*model.TranslationJob, time.Time, bool, error) {
	claimed, err := m.tryClaimTranslationDelivery(ctx, job.ID, generator, resumeRunning)
	if err != nil {
		return job, time.Time{}, false, err
	}
	if !claimed {
		return m.handleUnclaimedTranslationDelivery(ctx, job.ID)
	}
	claimedAt := m.now().UTC()
	job.Status = translationJobStatusRunning
	job, err = m.loadTranslationJob(ctx, job.ID)
	if err != nil {
		return job, claimedAt, false, m.handleRunningDeliveryFailure(ctx, job, claimedAt, err)
	}
	if job == nil {
		return nil, claimedAt, false, nil
	}
	return job, translationJobStartedAt(job, m.now().UTC()), true, nil
}

func (m *TranslationJobManager) tryClaimTranslationDelivery(
	ctx context.Context,
	jobID string,
	generator translation.Generator,
	resumeRunning bool,
) (bool, error) {
	if resumeRunning {
		return m.resumeTranslationJob(ctx, jobID, generator)
	}
	return m.claimTranslationJob(ctx, jobID, generator)
}

func (m *TranslationJobManager) handleUnclaimedTranslationDelivery(
	ctx context.Context,
	jobID string,
) (*model.TranslationJob, time.Time, bool, error) {
	job, err := m.loadTranslationJob(ctx, jobID)
	if err != nil {
		return job, time.Time{}, false, err
	}
	return job, time.Time{}, false, err
}

func (m *TranslationJobManager) runClaimedTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
	generators []translation.Generator,
) error {
	m.metrics.recordJobStatus(ctx, execution.job, translationJobStatusRunning)

	proceed, err := m.prepareTranslationDelivery(ctx, execution)
	if err != nil || !proceed {
		return err
	}
	if err := m.generateTranslationDelivery(ctx, execution, generators); err != nil {
		return err
	}
	proceed, err = m.reloadTranslationDeliveryForApply(ctx, execution)
	if err != nil || !proceed {
		return err
	}
	return m.applyTranslationDelivery(ctx, execution)
}

func (m *TranslationJobManager) prepareTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
) (bool, error) {
	job := execution.job
	artifact, err := loadTranslationRequestArtifact(ctx, m.db, job.ID)
	if err != nil {
		return false, m.failTranslationDelivery(ctx, execution, err)
	}
	request, plan, err := translation.ParseRequestArtifact(artifact)
	if err != nil {
		return false, m.failTranslationDelivery(ctx, execution, err)
	}
	request.RequestID = job.ID
	request.OperationID = job.OperationID
	if artifact.Digest != job.RequestArtifactDigest ||
		plan.EntityType != job.EntityType || plan.EntityID != job.EntityID ||
		plan.SourceLocale != job.SourceLocale || plan.TargetLocale != job.TargetLocale {
		return false, m.failTranslationDelivery(
			ctx, execution, errors.New("translation request artifact does not match the job"),
		)
	}
	execution.plan = plan
	execution.request = request
	return true, nil
}

func (m *TranslationJobManager) generateTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
	generators []translation.Generator,
) error {
	response, generator, err := m.generateValidatedTranslation(
		ctx,
		execution.job,
		execution.request,
		generators,
		execution.allowExpiredDocumentReplacement,
	)
	if err != nil {
		return m.failTranslationDelivery(ctx, execution, err)
	}
	execution.response = response
	execution.generator = generator
	if err := m.updateRunningJobProvider(ctx, execution.job.ID, generator); err != nil {
		if errors.Is(err, errTranslationJobNoLongerCurrent) {
			terminal, terminalErr := m.translationDeliveryAlreadyTerminal(ctx, execution.job.ID)
			if terminalErr != nil {
				return terminalErr
			}
			if terminal {
				return nil
			}
		}
		return m.failTranslationDelivery(ctx, execution, err)
	}
	return nil
}

func (m *TranslationJobManager) translationDeliveryAlreadyTerminal(
	ctx context.Context,
	jobID string,
) (bool, error) {
	job, err := m.loadTranslationJob(ctx, jobID)
	if err != nil {
		return false, err
	}
	// Terminal jobs are removed with their transport artifacts. A delivery
	// that already loaded the job therefore treats a missing row as terminal.
	return job == nil, nil
}

func (m *TranslationJobManager) reloadTranslationDeliveryForApply(
	ctx context.Context,
	execution *translationDeliveryExecution,
) (bool, error) {
	job := execution.job
	job, err := m.loadTranslationJob(ctx, job.ID)
	if err != nil {
		return false, m.failTranslationDelivery(ctx, execution, err)
	}
	execution.job = job
	if job == nil || job.Status != translationJobStatusRunning {
		return false, nil
	}
	currentSource, err := m.loadTranslationSourceDocument(ctx, job.EntityType, job.EntityID)
	if err != nil {
		return false, m.failTranslationDelivery(ctx, execution, err)
	}
	// The request artifact owns the request-time unit set even if the root
	// source-locale pointer later changes. Candidate/domain adapters intersect
	// those stable handles with the current locked graph at persistence time;
	// current source text or locale must not turn a still-existing unit into a
	// different request.
	execution.sourceDoc = currentSource
	return true, nil
}

func (m *TranslationJobManager) applyTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
) error {
	candidate, err := buildTranslationCandidateContent(m.domains, execution.plan, execution.sourceDoc, execution.response)
	if err != nil {
		return m.failTranslationDelivery(ctx, execution, markTranslationTargetApplyFailure(err))
	}
	if err := candidate.SetProviderUnitPatch(execution.plan, translation.XLIFFTargets(execution.response.Document)); err != nil {
		return m.failTranslationDelivery(ctx, execution, markTranslationTargetApplyFailure(err))
	}
	now := m.now().UTC()
	applied, err := m.applyAppliedTranslationResult(
		ctx,
		execution.job,
		candidate,
		now,
	)
	if err != nil {
		return m.failTranslationDelivery(ctx, execution, err)
	}
	if !applied {
		return nil
	}
	return m.finalizeAppliedTranslationDelivery(ctx, execution, now)
}

func (m *TranslationJobManager) finalizeAppliedTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
	now time.Time,
) error {
	job := execution.job
	m.publishLifecycle(ctx, job, managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_APPLIED, nil)
	m.metrics.recordJobStatus(ctx, job, translationJobStatusApplied)
	m.metrics.recordJobDuration(ctx, job, translationJobStatusApplied, execution.startedAt, now)
	emitTranslationJobTerminal(
		ctx,
		job,
		translationJobTerminalOutcomeApplied,
		"",
		execution.startedAt,
		now,
	)
	return nil
}

func (m *TranslationJobManager) failTranslationDelivery(
	ctx context.Context,
	execution *translationDeliveryExecution,
	cause error,
) error {
	return m.handleRunningDeliveryFailure(
		ctx,
		execution.job,
		execution.startedAt,
		cause,
	)
}
