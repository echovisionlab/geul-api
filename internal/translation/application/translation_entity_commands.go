package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *TranslationService) ListEntityTranslations(
	ctx context.Context,
	req *connect.Request[managev1.ListEntityTranslationsRequest],
) (*connect.Response[managev1.ListEntityTranslationsResponse], error) {
	entityType, entityID, err := parseTranslationTarget(req.Msg.Target)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return nil, err
	}

	authority, err := loadTranslationDocumentAuthority(ctx, s.db, entityType, entityID)
	if err != nil {
		return nil, err
	}

	entries, err := s.listEntityTranslations(ctx, entityType, entityID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListEntityTranslationsResponse{
		Entries:      entries,
		SourceLocale: authority.SourceLocale,
	}), nil
}

func (s *TranslationService) GetEntityTranslation(
	ctx context.Context,
	req *connect.Request[managev1.GetEntityTranslationRequest],
) (*connect.Response[managev1.GetEntityTranslationResponse], error) {
	entityType, entityID, err := parseTranslationTarget(req.Msg.Target)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTranslationRead(ctx, entityType, entityID); err != nil {
		return nil, err
	}

	entry, err := s.getEntityTranslation(ctx, entityType, entityID, strings.TrimSpace(req.Msg.Locale))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetEntityTranslationResponse{Entry: entry}), nil
}

func (s *TranslationService) SetEntitySourceLocale(
	ctx context.Context,
	req *connect.Request[managev1.SetEntitySourceLocaleRequest],
) (*connect.Response[managev1.SetEntitySourceLocaleResponse], error) {
	entityType, entityID, err := parseTranslationTarget(req.Msg.Target)
	if err != nil {
		return nil, err
	}
	if s.domains == nil {
		return nil, errs.InternalMsg("translation domain registry is required")
	}
	sourceLocale := strings.TrimSpace(req.Msg.SourceLocale)
	if err := validateRequestedSourceLocale(ctx, s.db, sourceLocale); err != nil {
		return nil, err
	}
	expectedDocumentRevision, err := parseExpectedTranslationDocumentRevision(
		entityType,
		req.Msg.GetExpectedDocumentRevision(),
	)
	if err != nil {
		return nil, err
	}
	requestingMemberID, err := requireAuthenticatedTranslationRequester(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.applySourceLocaleSwitch(
		ctx,
		entityType,
		entityID,
		sourceLocale,
		expectedDocumentRevision,
		requestingMemberID,
	)
	if err != nil {
		return nil, err
	}
	response := &managev1.SetEntitySourceLocaleResponse{
		DocumentRevision: result.documentRevision,
		Changed:          !result.unchanged,
		SourceChanged:    !result.unchanged,
		ChangedLocales:   result.changedLocales,
	}
	if result.unchanged {
		return connect.NewResponse(response), nil
	}
	if event := buildTypedSourceLocaleUpdatedEvent(
		entityType,
		entityID,
		result.documentRevision,
	); event != nil {
		if publishErr := s.publisher.PublishContentUpdated(ctx, event); publishErr != nil {
			slog.Warn("Failed to publish source locale content updated event",
				"error", publishErr,
				"entityType", entityType,
				"entityId", entityID,
			)
		}
	}
	return connect.NewResponse(response), nil
}

func (s *TranslationService) RegenerateEntityTranslations(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateEntityTranslationsRequest],
) (*connect.Response[managev1.RegenerateEntityTranslationsResponse], error) {
	action := "regenerate_locale"
	if len(req.Msg.GetLocales()) == 0 {
		action = "regenerate_all"
	}
	outcome := "failed"
	defer func() { s.metrics.recordAdminAction(ctx, action, outcome) }()
	requestedBy, err := requireAuthenticatedTranslationRequester(ctx)
	if err != nil {
		return nil, err
	}

	entityType, entityID, err := parseTranslationTarget(req.Msg.Target)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	operationID := generateUUID()
	result, err := s.createRegeneratedTranslationJobs(
		ctx, entityType, entityID, req.Msg.GetLocales(), requestedBy, operationID, now,
	)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	for index := range result.createdJobs {
		s.metrics.recordQueuedJob(ctx, &result.createdJobs[index])
	}
	created, err := s.publishRegeneratedTranslationJobs(result.jobs)
	if err != nil {
		return nil, err
	}
	outcome = "succeeded"
	return connect.NewResponse(&managev1.RegenerateEntityTranslationsResponse{Jobs: created}), nil
}

type regeneratedTranslationJobsResult struct {
	jobs        []model.TranslationJob
	createdJobs []model.TranslationJob
}

func (s *TranslationService) createRegeneratedTranslationJobs(
	ctx context.Context,
	entityType string,
	entityID string,
	requestedLocales []string,
	requestedBy string,
	operationID string,
	now time.Time,
) (regeneratedTranslationJobsResult, error) {
	var result regeneratedTranslationJobsResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.validateTranslationRegenerationWithDB(ctx, tx, entityType, entityID); err != nil {
			return err
		}
		if s.domains == nil {
			return errs.InternalMsg("translation domain registry is required")
		}
		authority, err := loadTranslationDocumentAuthority(ctx, tx, entityType, entityID)
		if err != nil {
			return err
		}
		sourceDocument, err := s.domains.LoadSourceDocument(
			ctx, tx, s.contentBlocks, entityType, entityID,
		)
		if err != nil {
			return err
		}
		sourceDocument.SourceLocale = authority.SourceLocale
		sourceDocument.ContentDocumentRevision = authority.DocumentRevision.String()
		sourceLocale := strings.TrimSpace(sourceDocument.SourceLocale)
		if sourceLocale == "" {
			return errs.InternalMsg("translation source document locale is required")
		}
		targetLocales, err := s.resolveRequestedLocales(ctx, sourceLocale, requestedLocales)
		if err != nil {
			return err
		}
		if err := s.requireAvailableTranslationProvider(ctx); err != nil {
			return err
		}
		for _, locale := range targetLocales {
			job, created, err := createRegeneratedTranslationJobWithDB(
				ctx, tx, entityType, entityID, sourceLocale, locale, requestedBy, operationID, now,
				func(job *model.TranslationJob) (translation.RequestArtifact, error) {
					return buildPersistedTranslationRequest(
						ctx, tx, s.domains, job, sourceDocument,
					)
				},
			)
			if err != nil {
				return err
			}
			result.jobs = append(result.jobs, job)
			if !created {
				continue
			}
			result.createdJobs = append(result.createdJobs, job)
			if err := enqueueTranslationGenerateWithDB(
				ctx,
				s.publisher,
				tx,
				&managev1.TranslationGenerateEvent{JobId: job.ID},
			); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func (s *TranslationService) validateTranslationRegenerationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) error {
	if s.domains == nil {
		return errs.InternalMsg("translation domain registry is required")
	}
	if err := s.domains.LockRoot(ctx, tx, entityType, entityID); err != nil {
		return err
	}
	if err := requireEditableTranslationDomain(ctx, tx, s.domains, entityType, entityID); err != nil {
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
		return model.TranslationJob{}, false, errs.Internal(err)
	}
	if _, ok := translation.DefinitionForKind(entityType); !ok {
		return model.TranslationJob{}, false, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	if buildArtifact == nil {
		return model.TranslationJob{}, false, errs.InternalMsg("translation request artifact builder is required")
	}
	artifact, err := buildArtifact(&job)
	if err != nil {
		return model.TranslationJob{}, false, err
	}
	if len(artifact.XLIFF) == 0 || len(artifact.Manifest) == 0 {
		return model.TranslationJob{}, false, errs.InternalMsg("translation request artifact is incomplete")
	}
	if artifact.Digest != translation.RequestArtifactDigest(artifact.XLIFF, artifact.Manifest) {
		return model.TranslationJob{}, false, errs.InternalMsg("translation request artifact digest is invalid")
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
			return model.TranslationJob{}, false, errs.Internal(err)
		}
		return activeJobs[len(activeJobs)-1], false, nil
	}
	insertErr := tx.WithContext(ctx).Transaction(func(insertTx *gorm.DB) error {
		return insertTx.Create(&job).Error
	})
	if insertErr == nil {
		return job, true, nil
	}
	if !dberrors.IsUniqueViolation(insertErr) {
		return model.TranslationJob{}, false, insertErr
	}
	activeJobs, err = lockActiveTranslationRetryJobs(tx.WithContext(ctx), job)
	if err != nil {
		return model.TranslationJob{}, false, err
	}
	if len(activeJobs) == 0 {
		return model.TranslationJob{}, false, insertErr
	}
	return activeJobs[len(activeJobs)-1], false, nil
}

func (s *TranslationService) publishRegeneratedTranslationJobs(
	jobs []model.TranslationJob,
) ([]*managev1.TranslationJob, error) {
	created := make([]*managev1.TranslationJob, 0, len(jobs))
	for index := range jobs {
		job := &jobs[index]
		created = append(created, toProtoTranslationJob(*job))
	}
	return created, nil
}
