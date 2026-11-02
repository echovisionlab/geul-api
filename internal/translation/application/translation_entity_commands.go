package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// ListEntityTranslationsQuery lists the stored locales of one entity.
type ListEntityTranslationsQuery struct {
	Target *Target
}

// EntityTranslations are the stored locale entries of one entity.
type EntityTranslations struct {
	SourceLocale string
	Entries      []Entry
}

// ListEntityTranslations lists the stored locale entries of an entity.
func (s *Service) ListEntityTranslations(ctx context.Context, query ListEntityTranslationsQuery) (EntityTranslations, error) {
	entityType, entityID, err := parseTranslationTarget(query.Target)
	if err != nil {
		return EntityTranslations{}, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return EntityTranslations{}, err
	}
	var translations EntityTranslations
	err = s.units.Read(ctx, func(store Store) error {
		authority, err := loadTranslationDocumentAuthority(ctx, store, entityType, entityID)
		if err != nil {
			return err
		}
		entries, err := store.Entries(ctx, entityType, entityID)
		if err != nil {
			return err
		}
		translations = EntityTranslations{SourceLocale: authority.SourceLocale, Entries: entries}
		return nil
	})
	if err != nil {
		return EntityTranslations{}, err
	}
	if translations.Entries == nil {
		translations.Entries = []Entry{}
	}
	return translations, nil
}

// GetEntityTranslationQuery reads one stored locale of an entity.
type GetEntityTranslationQuery struct {
	Target *Target
	Locale string
}

// GetEntityTranslation reads one stored locale entry of an entity.
func (s *Service) GetEntityTranslation(ctx context.Context, query GetEntityTranslationQuery) (Entry, error) {
	entityType, entityID, err := parseTranslationTarget(query.Target)
	if err != nil {
		return Entry{}, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return Entry{}, err
	}
	locale := strings.TrimSpace(query.Locale)
	var entry Entry
	err = s.units.Read(ctx, func(store Store) error {
		var err error
		entry, err = store.Entry(ctx, entityType, entityID, locale)
		return err
	})
	if errors.Is(err, ErrEntryNotFound) {
		return Entry{}, domainerr.ResourceNotFound("translation_entry", entityID+":"+locale)
	}
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// SetEntitySourceLocaleCommand makes SourceLocale the source of an entity's
// Content Document, guarded by the document revision the caller read.
type SetEntitySourceLocaleCommand struct {
	Target                   *Target
	SourceLocale             string
	ExpectedDocumentRevision string
}

// SourceLocaleChange is the outcome of a source-locale switch.
type SourceLocaleChange struct {
	DocumentRevision string
	Changed          bool
	ChangedLocales   []string
}

// SetEntitySourceLocale switches an entity's source locale.
func (s *Service) SetEntitySourceLocale(ctx context.Context, command SetEntitySourceLocaleCommand) (SourceLocaleChange, error) {
	entityType, entityID, err := parseTranslationTarget(command.Target)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	sourceLocale := strings.TrimSpace(command.SourceLocale)
	if err := s.validateRequestedSourceLocale(ctx, sourceLocale); err != nil {
		return SourceLocaleChange{}, err
	}
	expectedDocumentRevision, err := parseExpectedTranslationDocumentRevision(command.ExpectedDocumentRevision)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	requestingMemberID, err := requireAuthenticatedTranslationRequester(ctx)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	result, err := s.applySourceLocaleSwitch(ctx, entityType, entityID, sourceLocale, expectedDocumentRevision, requestingMemberID)
	if err != nil {
		return SourceLocaleChange{}, err
	}
	return SourceLocaleChange{
		DocumentRevision: result.documentRevision,
		Changed:          !result.unchanged,
		ChangedLocales:   result.changedLocales,
	}, nil
}

// RegenerateEntityTranslationsCommand queues machine translation of an
// entity into Locales, or into every machine-translation target when empty.
type RegenerateEntityTranslationsCommand struct {
	Target  *Target
	Locales []string
}

// RegenerateEntityTranslations queues a job per target locale and returns the
// queued jobs, reusing an active job for the same request.
func (s *Service) RegenerateEntityTranslations(ctx context.Context, command RegenerateEntityTranslationsCommand) ([]Job, error) {
	action := "regenerate_locale"
	if len(command.Locales) == 0 {
		action = "regenerate_all"
	}
	outcome := "failed"
	defer func() { s.metrics.recordAdminAction(ctx, action, outcome) }()
	requestedBy, err := requireAuthenticatedTranslationRequester(ctx)
	if err != nil {
		return nil, err
	}

	entityType, entityID, err := parseTranslationTarget(command.Target)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	operationID := generateUUID()
	result, err := s.createRegeneratedTranslationJobs(
		ctx, entityType, entityID, command.Locales, requestedBy, operationID, now,
	)
	if err != nil {
		return nil, err
	}
	for index := range result.createdJobs {
		s.metrics.recordQueuedJob(ctx, &result.createdJobs[index])
	}
	outcome = "succeeded"
	jobs := result.jobs
	if jobs == nil {
		jobs = []Job{}
	}
	return jobs, nil
}

type regeneratedTranslationJobsResult struct {
	jobs        []Job
	createdJobs []Job
}

func (s *Service) createRegeneratedTranslationJobs(
	ctx context.Context,
	entityType string,
	entityID string,
	requestedLocales []string,
	requestedBy string,
	operationID string,
	now time.Time,
) (regeneratedTranslationJobsResult, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[regeneratedTranslationJobsResult, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return store.Domains().LockRoot(ctx, entityType, entityID)
		},
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			return requireTranslationRegenerationAuthority(ctx, store, entityType, entityID)
		}),
		Change: func(ctx context.Context, store Store) (regeneratedTranslationJobsResult, error) {
			return s.queueRegeneratedTranslationJobs(ctx, store, entityType, entityID, requestedLocales, requestedBy, operationID, now)
		},
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(regeneratedTranslationJobsResult) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store Store, result regeneratedTranslationJobsResult) error {
			for index := range result.createdJobs {
				if err := store.EnqueueGenerate(ctx, result.createdJobs[index].ID); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (s *Service) queueRegeneratedTranslationJobs(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
	requestedLocales []string,
	requestedBy string,
	operationID string,
	now time.Time,
) (regeneratedTranslationJobsResult, error) {
	var result regeneratedTranslationJobsResult
	authority, err := loadTranslationDocumentAuthority(ctx, store, entityType, entityID)
	if err != nil {
		return result, err
	}
	sourceDocument, err := store.Domains().LoadSourceDocument(ctx, entityType, entityID)
	if err != nil {
		return result, err
	}
	sourceDocument.SourceLocale = authority.SourceLocale
	sourceDocument.ContentDocumentRevision = authority.DocumentRevision.String()
	sourceLocale := strings.TrimSpace(sourceDocument.SourceLocale)
	if sourceLocale == "" {
		return result, errors.New("translation source document locale is required")
	}
	targetLocales, err := s.resolveRequestedLocales(ctx, store, sourceLocale, requestedLocales)
	if err != nil {
		return result, err
	}
	if err := requireAvailableTranslationProvider(ctx, store); err != nil {
		return result, err
	}
	for _, locale := range targetLocales {
		job, created, err := createRegeneratedTranslationJob(
			ctx, store, entityType, entityID, sourceLocale, locale, requestedBy, operationID, now,
			func(job *Job) (translation.RequestArtifact, error) {
				return buildPersistedTranslationRequest(ctx, store, s.plans, job, sourceDocument)
			},
		)
		if err != nil {
			return result, err
		}
		result.jobs = append(result.jobs, job)
		if created {
			result.createdJobs = append(result.createdJobs, job)
		}
	}
	return result, nil
}

// requireTranslationRegenerationAuthority decides, under the locked root,
// whether the principal may queue or cancel machine translation of the entity.
func requireTranslationRegenerationAuthority(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
) error {
	domains := store.Domains()
	if err := domains.RequireEditable(ctx, entityType, entityID); err != nil {
		return err
	}
	if entityType == "privacy" || entityType == "terms" {
		return domains.RequireLegalEditable(ctx, entityType, entityID)
	}
	return domains.RequireSourceLocaleEdit(ctx, entityType, entityID)
}

func createRegeneratedTranslationJob(
	ctx context.Context,
	store Store,
	entityType string,
	entityID string,
	sourceLocale string,
	targetLocale string,
	requestedBy string,
	operationID string,
	now time.Time,
	buildArtifact func(*Job) (translation.RequestArtifact, error),
) (Job, bool, error) {
	requestedBy = strings.TrimSpace(requestedBy)
	job := Job{
		ID: generateUUID(), EntityType: entityType, EntityID: entityID, TargetLocale: targetLocale,
		SourceLocale: sourceLocale,
		OperationID:  operationID,
		Status:       JobQueued, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		RequestedByMemberID: requestedBy,
	}
	if err := ValidateJobRequester(&job); err != nil {
		return Job{}, false, err
	}
	if _, ok := translation.DefinitionForKind(entityType); !ok {
		return Job{}, false, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	if buildArtifact == nil {
		return Job{}, false, errors.New("translation request artifact builder is required")
	}
	artifact, err := buildArtifact(&job)
	if err != nil {
		return Job{}, false, err
	}
	if len(artifact.XLIFF) == 0 || len(artifact.Manifest) == 0 {
		return Job{}, false, errors.New("translation request artifact is incomplete")
	}
	if artifact.Digest != translation.RequestArtifactDigest(artifact.XLIFF, artifact.Manifest) {
		return Job{}, false, errors.New("translation request artifact digest is invalid")
	}
	job.RequestXLIFF = artifact.XLIFF
	job.RequestManifest = artifact.Manifest
	job.RequestArtifactDigest = artifact.Digest
	if err := validateTranslationJobRequestArtifact(&job); err != nil {
		return Job{}, false, err
	}
	key := ActiveJobKey{
		EntityType: job.EntityType, EntityID: job.EntityID, TargetLocale: job.TargetLocale,
		RequestArtifactDigest: job.RequestArtifactDigest,
	}
	activeJobs, err := store.LockActiveJobs(ctx, key)
	if err != nil {
		return Job{}, false, err
	}
	if len(activeJobs) > 0 {
		if err := ValidateJobRequester(&activeJobs[len(activeJobs)-1]); err != nil {
			return Job{}, false, err
		}
		return activeJobs[len(activeJobs)-1], false, nil
	}
	// A concurrent request may have queued the same job since the lock above;
	// the active-job unique index then keeps its row and this one is dropped.
	inserted, err := store.InsertJob(ctx, job)
	if err != nil {
		return Job{}, false, err
	}
	if inserted {
		return job, true, nil
	}
	activeJobs, err = store.LockActiveJobs(ctx, key)
	if err != nil {
		return Job{}, false, err
	}
	if len(activeJobs) == 0 {
		return Job{}, false, errors.New("translation job insert conflicted without an active job")
	}
	return activeJobs[len(activeJobs)-1], false, nil
}
