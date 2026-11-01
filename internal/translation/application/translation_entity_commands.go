package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ListEntityTranslationsQuery lists the stored locales of one entity.
type ListEntityTranslationsQuery struct {
	Target *managev1.TranslationTarget
}

// EntityTranslations are the stored locale entries of one entity.
type EntityTranslations struct {
	SourceLocale string
	Entries      []*managev1.TranslationEntry
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
	authority, err := loadTranslationDocumentAuthority(ctx, s.db, entityType, entityID)
	if err != nil {
		return EntityTranslations{}, err
	}
	entries, err := s.listEntityTranslations(ctx, entityType, entityID)
	if err != nil {
		return EntityTranslations{}, err
	}
	return EntityTranslations{SourceLocale: authority.SourceLocale, Entries: entries}, nil
}

// GetEntityTranslationQuery reads one stored locale of an entity.
type GetEntityTranslationQuery struct {
	Target *managev1.TranslationTarget
	Locale string
}

// GetEntityTranslation reads one stored locale entry of an entity.
func (s *Service) GetEntityTranslation(ctx context.Context, query GetEntityTranslationQuery) (*managev1.TranslationEntry, error) {
	entityType, entityID, err := parseTranslationTarget(query.Target)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return nil, err
	}
	return s.getEntityTranslation(ctx, entityType, entityID, strings.TrimSpace(query.Locale))
}

// SetEntitySourceLocaleCommand makes SourceLocale the source of an entity's
// Content Document, guarded by the document revision the caller read.
type SetEntitySourceLocaleCommand struct {
	Target                   *managev1.TranslationTarget
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
	if err := validateRequestedSourceLocale(ctx, s.db, sourceLocale); err != nil {
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
	Target  *managev1.TranslationTarget
	Locales []string
}

// RegenerateEntityTranslations queues a job per target locale and returns the
// queued jobs, reusing an active job for the same request.
func (s *Service) RegenerateEntityTranslations(ctx context.Context, command RegenerateEntityTranslationsCommand) ([]*managev1.TranslationJob, error) {
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
	return toProtoTranslationJobs(result.jobs), nil
}

type regeneratedTranslationJobsResult struct {
	jobs        []model.TranslationJob
	createdJobs []model.TranslationJob
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
	return mutation.Run(ctx, s.mutations, mutation.Spec[regeneratedTranslationJobsResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return s.domains.LockRoot(ctx, tx, entityType, entityID)
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return s.requireTranslationRegenerationAuthority(ctx, tx, entityType, entityID)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (regeneratedTranslationJobsResult, error) {
			return s.queueRegeneratedTranslationJobs(ctx, tx, entityType, entityID, requestedLocales, requestedBy, operationID, now)
		},
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(regeneratedTranslationJobsResult) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, tx *gorm.DB, result regeneratedTranslationJobsResult) error {
			for index := range result.createdJobs {
				if err := s.publisher.EnqueueTranslationGenerateWithDB(
					ctx, tx, &managev1.TranslationGenerateEvent{JobId: result.createdJobs[index].ID},
				); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (s *Service) queueRegeneratedTranslationJobs(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	requestedLocales []string,
	requestedBy string,
	operationID string,
	now time.Time,
) (regeneratedTranslationJobsResult, error) {
	var result regeneratedTranslationJobsResult
	authority, err := loadTranslationDocumentAuthority(ctx, tx, entityType, entityID)
	if err != nil {
		return result, err
	}
	sourceDocument, err := s.domains.LoadSourceDocument(ctx, tx, s.contentBlocks, entityType, entityID)
	if err != nil {
		return result, err
	}
	sourceDocument.SourceLocale = authority.SourceLocale
	sourceDocument.ContentDocumentRevision = authority.DocumentRevision.String()
	sourceLocale := strings.TrimSpace(sourceDocument.SourceLocale)
	if sourceLocale == "" {
		return result, errors.New("translation source document locale is required")
	}
	targetLocales, err := s.resolveRequestedLocales(ctx, sourceLocale, requestedLocales)
	if err != nil {
		return result, err
	}
	if err := s.requireAvailableTranslationProvider(ctx); err != nil {
		return result, err
	}
	for _, locale := range targetLocales {
		job, created, err := createRegeneratedTranslationJobWithDB(
			ctx, tx, entityType, entityID, sourceLocale, locale, requestedBy, operationID, now,
			func(job *model.TranslationJob) (translation.RequestArtifact, error) {
				return buildPersistedTranslationRequest(ctx, tx, s.domains, job, sourceDocument)
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
func (s *Service) requireTranslationRegenerationAuthority(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) error {
	if err := s.domains.RequireEditable(ctx, tx, entityType, entityID); err != nil {
		return err
	}
	if entityType == "privacy" || entityType == "terms" {
		return s.domains.RequireLegalEditable(ctx, tx, s.spiceDB, entityType, entityID)
	}
	return s.domains.RequireSourceLocaleEdit(ctx, tx, s.spiceDB, entityType, entityID)
}

func createRegeneratedTranslationJobWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	sourceLocale string,
	targetLocale string,
	requestedBy string,
	operationID string,
	now time.Time,
	buildArtifact func(*model.TranslationJob) (translation.RequestArtifact, error),
) (model.TranslationJob, bool, error) {
	requestedBy = strings.TrimSpace(requestedBy)
	job := model.TranslationJob{
		ID: generateUUID(), EntityType: entityType, EntityID: entityID, TargetLocale: targetLocale,
		SourceLocale: sourceLocale,
		OperationID:  operationID,
		Status:       translationJobStatusQueued, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		RequestedByMemberID: requestedBy,
	}
	if err := validateTranslationJobRequester(&job); err != nil {
		return model.TranslationJob{}, false, err
	}
	if _, ok := translation.DefinitionForKind(entityType); !ok {
		return model.TranslationJob{}, false, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	if buildArtifact == nil {
		return model.TranslationJob{}, false, errors.New("translation request artifact builder is required")
	}
	artifact, err := buildArtifact(&job)
	if err != nil {
		return model.TranslationJob{}, false, err
	}
	if len(artifact.XLIFF) == 0 || len(artifact.Manifest) == 0 {
		return model.TranslationJob{}, false, errors.New("translation request artifact is incomplete")
	}
	if artifact.Digest != translation.RequestArtifactDigest(artifact.XLIFF, artifact.Manifest) {
		return model.TranslationJob{}, false, errors.New("translation request artifact digest is invalid")
	}
	job.RequestXLIFF = artifact.XLIFF
	job.RequestManifest = artifact.Manifest
	job.RequestArtifactDigest = artifact.Digest
	if err := validateTranslationJobRequestArtifact(&job); err != nil {
		return model.TranslationJob{}, false, err
	}
	activeJobs, err := lockActiveTranslationRetryJobs(tx.WithContext(ctx), job)
	if err != nil {
		return model.TranslationJob{}, false, err
	}
	if len(activeJobs) > 0 {
		if err := validateTranslationJobRequester(&activeJobs[len(activeJobs)-1]); err != nil {
			return model.TranslationJob{}, false, err
		}
		return activeJobs[len(activeJobs)-1], false, nil
	}
	// A concurrent request may have queued the same job since the lock above;
	// the active-job unique index then keeps its row and this one is dropped.
	inserted := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
	if inserted.Error != nil {
		return model.TranslationJob{}, false, inserted.Error
	}
	if inserted.RowsAffected == 1 {
		return job, true, nil
	}
	activeJobs, err = lockActiveTranslationRetryJobs(tx.WithContext(ctx), job)
	if err != nil {
		return model.TranslationJob{}, false, err
	}
	if len(activeJobs) == 0 {
		return model.TranslationJob{}, false, errors.New("translation job insert conflicted without an active job")
	}
	return activeJobs[len(activeJobs)-1], false, nil
}

func toProtoTranslationJobs(jobs []model.TranslationJob) []*managev1.TranslationJob {
	responseJobs := make([]*managev1.TranslationJob, 0, len(jobs))
	for index := range jobs {
		job := &jobs[index]
		responseJobs = append(responseJobs, toProtoTranslationJob(*job))
	}
	return responseJobs
}
