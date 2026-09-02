package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/proto"
)

// InternalWorkService is the trusted typed Block boundary used by the
// collaboration runtime. Yjs state never crosses this persistence API.
type InternalWorkService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	auditWriter    domainaudit.Appender
	spiceDB        CollaborationPermissionChecker
	checkpoints    persistencecheckpoint.ContributorFence
	contentBlocks  *contentblock.Store
	mediaHydrator  AuthorizedContentBlockMediaHydrator
	og             OGRequests
}

type InternalWorkServiceOption func(*InternalWorkService)

func WithInternalWorkDomainAuditWriter(writer domainaudit.Appender) InternalWorkServiceOption {
	return func(service *InternalWorkService) {
		service.auditWriter = writer
	}
}

func WithInternalWorkContentBlockStore(store *contentblock.Store) InternalWorkServiceOption {
	return func(service *InternalWorkService) {
		service.contentBlocks = store
	}
}

func WithInternalWorkCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalWorkServiceOption {
	return func(service *InternalWorkService) { service.checkpoints = checkpoints }
}

func WithInternalWorkContentBlockMediaHydrator(
	hydrator AuthorizedContentBlockMediaHydrator,
) InternalWorkServiceOption {
	return func(service *InternalWorkService) {
		service.mediaHydrator = hydrator
	}
}

func NewInternalWorkService(
	db *gorm.DB,
	asyncPublisher AsyncPublisher,
	ogRequests OGRequests,
	spiceDB CollaborationPermissionChecker,
	options ...InternalWorkServiceOption,
) *InternalWorkService {
	if db == nil {
		panic("InternalWorkService: db is required")
	}
	if asyncPublisher == nil {
		panic("InternalWorkService: asyncPublisher is required")
	}
	if spiceDB == nil {
		panic("InternalWorkService: SpiceDB checker is required")
	}
	if ogRequests == nil {
		panic("InternalWorkService: OG requests are required")
	}
	service := &InternalWorkService{
		db:             db,
		asyncPublisher: asyncPublisher,
		og:             ogRequests,
		spiceDB:        spiceDB,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalWorkService) requireContentBlockStore() error {
	if s.contentBlocks == nil {
		return errs.InternalMsg("Work content Block store is not configured")
	}
	return nil
}

func (s *InternalWorkService) LoadWorkBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadWorkBlockDocumentRequest],
) (*connect.Response[intrav1.LoadWorkBlockDocumentResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if s.mediaHydrator == nil {
		return nil, errs.InternalMsg("Work Block media hydrator is not configured")
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	if _, err := parseWorkContentUUID("work_id", req.Msg.WorkId); err != nil {
		return nil, err
	}
	locale, err := normalizeWorkDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	var state workTargetLocaleState
	var mediaItems []*contentv1.ContentBlockMediaItem
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		principal, authErr := requireWorkLoadPrincipal(
			ctx, tx, s.spiceDB, req.Msg.WorkId, documentID, req.Msg.Principal,
		)
		if authErr != nil {
			return authErr
		}
		var loadErr error
		state, loadErr = loadWorkTargetLocaleState(ctx, tx, s.contentBlocks, req.Msg.WorkId, documentID, locale, false)
		if loadErr != nil {
			return loadErr
		}
		mediaItems, loadErr = LoadContentBlockMediaReferences(ctx, tx, documentID)
		if loadErr != nil {
			return loadErr
		}
		mediaItems, loadErr = s.mediaHydrator.HydrateAuthorizedWorkBlockMediaWithDB(
			auth.WithUser(ctx, principal), tx, req.Msg.WorkId, documentID, principal, mediaItems,
		)
		return loadErr
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	if state.SourceMetadata.Title == nil || *state.SourceMetadata.Title == "" {
		return nil, errs.FailedPrecondition("Work source title is not initialized")
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	response := &intrav1.LoadWorkBlockDocumentResponse{
		Document:         document,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.WorkLocaleMetadata{
			Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary,
		},
		BlockMedia:   mediaItems,
		Locale:       locale,
		LocaleExists: locale == state.SourceLocale || state.TargetMetadata != nil,
	}
	if locale == state.SourceLocale {
		response.LocaleMetadata = response.SourceMetadata
	} else if state.TargetMetadata != nil {
		projected := workTargetMetadataProjection(state.SourceMetadata, state.TargetMetadata, locale)
		response.LocaleMetadata = &intrav1.WorkLocaleMetadata{
			Locale: projected.Locale, Title: projected.Title, Summary: projected.Summary,
		}
		response.TargetRevision = &state.TargetRevision
	}
	response.PresentLocaleValues = presentLocaleValues
	return connect.NewResponse(response), nil
}

type workBlockApplyResult struct {
	Content        contentblock.Result
	TargetRevision *string
	Locale         string
}

func (s *InternalWorkService) ApplyWorkBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyWorkBlockBatchRequest],
) (*connect.Response[intrav1.ApplyWorkBlockBatchResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	if _, err := parseWorkContentUUID("work_id", req.Msg.WorkId); err != nil {
		return nil, err
	}
	locale, err := normalizeWorkDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.Batch.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		req.Msg.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, errs.InvalidArgument("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		req.Msg.Batch.Profile,
		locale,
		&storage,
		req.Msg.AffectedLocaleValues,
	); err != nil {
		return nil, errs.InvalidArgument("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(
		documentID,
		req.Msg.Batch.Profile,
		storage,
	)
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}

	var output workBlockApplyResult
	var sourceLocale string
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := internalWorkContentFence(
			s.checkpoints, req.Msg.Batch.ContributorMemberIds,
		)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		authorizedFence := workLockedTargetFence(documentID, domain)
		sourceLocale = domain.SourceLocale
		output.Locale = locale
		if locale != domain.SourceLocale {
			result, targetRevision, targetErr := applyWorkTargetLocaleBatch(
				ctx, tx, s.contentBlocks, req.Msg.WorkId, documentID, locale, batch,
				req.Msg.ExpectedTargetRevision, workTargetMetadataPatch{},
				false, false, false, false, now,
				authorizedFence,
			)
			if targetErr != nil {
				return targetErr
			}
			output.Content, output.TargetRevision = result, targetRevision
			if result.Changed {
				return appendWorkMemberLocaleContentAudit(
					ctx, tx, s.auditWriter, contributorMemberID, req.Msg.WorkId, locale,
					sharedtelemetry.AuditItemOperationUpdated,
				)
			}
			return nil
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Work room cannot carry a target revision")
		}
		if err := validateWorkSourceStorage(storage, domain.SourceLocale); err != nil {
			return err
		}
		result, applyErr := s.contentBlocks.ApplyBatch(
			ctx,
			tx,
			batch,
			authorizedFence,
		)
		if applyErr != nil {
			return applyErr
		}
		output.Content = result
		for _, changedLocale := range result.ChangedLocales {
			if changedLocale != domain.SourceLocale {
				return errs.FailedPrecondition("target Work locale changed while saving source content")
			}
		}
		if result.Changed {
			if updateErr := tx.WithContext(ctx).Model(&model.Work{}).
				Where("id = ?", req.Msg.WorkId).
				UpdateColumn("updated_at", now).Error; updateErr != nil {
				return updateErr
			}
			return appendWorkMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.WorkId, locale,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		return nil
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}

	if output.Content.Changed {
		publishWorkContentUpdated(ctx, s.asyncPublisher, buildWorkSourceContentUpdatedEvent(
			req.Msg.WorkId,
			false,
			false,
			true,
			output.Content.DocumentRevision.String(),
			req.Msg.Batch.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			output.TargetRevision,
			locale == sourceLocale,
		))
	}
	return connect.NewResponse(&intrav1.ApplyWorkBlockBatchResponse{
		DocumentRevision: output.Content.DocumentRevision.String(), Changed: output.Content.Changed,
		SourceChanged:  output.Content.TranslationSourceChanged,
		ChangedLocales: output.Content.ChangedLocales,
		Locale:         locale, TargetRevision: output.TargetRevision,
	}), nil
}

type workMetadataUpdateResult struct {
	Advance        contentblock.AdvanceResult
	Target         contentblock.Result
	TargetRevision *string
	Mutation       workLocaleMetadataMutationResult
	OgRunID        *string
}

func (s *InternalWorkService) UpdateWorkLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateWorkLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateWorkLocaleMetadataResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	contributors, err := workContributorUUIDs(req.Msg.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.ContributorMemberIds)
	if err != nil {
		return nil, err
	}

	var output workMetadataUpdateResult
	var plan workLocaleMetadataMutationPlan
	var sourceLocale string
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := internalWorkContentFence(
			s.checkpoints, req.Msg.ContributorMemberIds,
		)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		sourceLocale = domain.SourceLocale
		var planErr error
		plan, planErr = planWorkLocaleMetadataMutation(req.Msg, sourceLocale)
		if planErr != nil {
			return planErr
		}
		authorizedFence := workLockedTargetFence(documentID, domain)
		if plan.Locale != sourceLocale {
			result, targetRevision, targetErr := applyWorkTargetLocaleBatch(
				ctx, tx, s.contentBlocks, req.Msg.WorkId, documentID, plan.Locale,
				contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: expectedRevision,
					ContributorMemberIDs: contributors,
				},
				req.Msg.ExpectedTargetRevision,
				workTargetMetadataPatch{
					UpdateTitle: plan.UpdateTitle, Title: plan.Title,
					UpdateSummary: plan.UpdateSummary, Summary: plan.Summary,
				},
				false, false, false, false, now,
				authorizedFence,
			)
			if targetErr != nil {
				return targetErr
			}
			output.Target, output.TargetRevision = result, targetRevision
			if !result.Changed {
				return nil
			}
			output.Mutation.TitleChanged = plan.UpdateTitle
			output.Mutation.SummaryChanged = plan.UpdateSummary
			if err := appendWorkMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.WorkId, plan.Locale,
				sharedtelemetry.AuditItemOperationUpdated,
			); err != nil {
				return err
			}
			if plan.UpdateTitle {
				runID, requestErr := s.og.RequestCurrentWithDB(
					ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
					req.Msg.WorkId, plan.Locale, false, "work_target_locale_metadata_saved",
				)
				if runID != "" {
					output.OgRunID = &runID
				}
				return requestErr
			}
			return nil
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Work metadata cannot carry a target revision")
		}
		var advanceErr error
		output.Advance, advanceErr = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			workSourceRoomFence(plan.Locale, authorizedFence),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				mutation, mutationErr := applyWorkLocaleMetadataMutation(ctx, tx, req.Msg.WorkId, plan, now)
				output.Mutation = mutation
				return mutation.Effect, mutationErr
			},
		)
		if advanceErr != nil {
			return advanceErr
		}
		if !output.Advance.Changed {
			return nil
		}
		if updateErr := tx.WithContext(ctx).Model(&model.Work{}).
			Where("id = ?", req.Msg.WorkId).
			UpdateColumn("updated_at", now).Error; updateErr != nil {
			return updateErr
		}
		if output.Advance.TranslationSourceChanged {
			runID, requestErr := s.og.RequestCurrentWithDB(
				ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
				req.Msg.WorkId, plan.Locale, false, "work_locale_metadata_saved",
			)
			if runID != "" {
				output.OgRunID = &runID
			}
			if requestErr != nil {
				return requestErr
			}
		}
		return appendWorkMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, contributorMemberID, req.Msg.WorkId, plan.Locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}

	changed := output.Advance.Changed || output.Target.Changed
	documentRevision := output.Advance.DocumentRevision.String()
	sourceChanged := output.Advance.TranslationSourceChanged
	changedLocales := []string{plan.Locale}
	if plan.Locale != sourceLocale {
		documentRevision = output.Target.DocumentRevision.String()
		sourceChanged = false
	}
	if changed {
		publishWorkContentUpdated(ctx, s.asyncPublisher, buildWorkSourceContentUpdatedEvent(
			req.Msg.WorkId,
			output.Mutation.TitleChanged,
			output.Mutation.SummaryChanged,
			false,
			documentRevision,
			req.Msg.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			plan.Locale,
			true,
			output.TargetRevision,
			plan.Locale == sourceLocale,
		))
	}
	return connect.NewResponse(&intrav1.UpdateWorkLocaleMetadataResponse{
		DocumentRevision: documentRevision, Changed: changed,
		SourceChanged: sourceChanged, ChangedLocales: changedLocales,
		Locale: plan.Locale, TargetRevision: output.TargetRevision,
	}), nil
}

func (s *InternalWorkService) CreateWorkVersionCheckpoint(
	ctx context.Context,
	req *connect.Request[intrav1.CreateWorkVersionCheckpointRequest],
) (*connect.Response[intrav1.CreateWorkVersionCheckpointResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	locale, err := normalizeWorkDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parseWorkContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	contributors := normalizeContributorMemberIDs(req.Msg.ContributorMemberIds)
	if !slices.Equal([]string(contributors), req.Msg.ContributorMemberIds) {
		return nil, errs.InvalidArgument("contributor_member_ids", "must be sorted and unique")
	}

	var created *model.WorkVersion
	var revision string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		advance, advanceErr := s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			internalWorkContentFence(s.checkpoints, req.Msg.ContributorMemberIds),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				return contentblock.MetadataEffect{}, nil
			},
		)
		if advanceErr != nil {
			return advanceErr
		}
		revision = advance.DocumentRevision.String()
		source, sourceErr := LoadRequiredSourceLocaleMetadata(ctx, tx, req.Msg.WorkId)
		if sourceErr != nil {
			return sourceErr
		}
		if locale != source.Locale {
			return errs.InvalidArgument("locale", "Work version checkpoints are source-locale only")
		}
		if source.Title == nil || *source.Title == "" {
			return errs.FailedPrecondition("Work source title is not initialized")
		}
		snapshot, snapshotErr := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, source.Locale)
		if snapshotErr != nil {
			return snapshotErr
		}
		document, documentErr := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, source.Locale)
		if documentErr != nil {
			return documentErr
		}
		encoded, encodeErr := EncodeVersionContentSnapshot(
			source.Locale,
			source.Title,
			source.Summary,
			document,
		)
		if encodeErr != nil {
			return encodeErr
		}

		var previous model.WorkVersion
		previousResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("work_id = ?", req.Msg.WorkId).
			Order("version DESC").
			First(&previous)
		if previousResult.Error != nil && !errors.Is(previousResult.Error, gorm.ErrRecordNotFound) {
			return previousResult.Error
		}
		if previousResult.Error == nil {
			same, sameErr := sameWorkVersionContentSnapshot(previous.ContentSnapshot, encoded)
			if sameErr != nil {
				return sameErr
			}
			if same {
				return nil
			}
		}

		nextVersion := int32(1)
		if previousResult.Error == nil {
			nextVersion = previous.Version + 1
		}
		created = &model.WorkVersion{
			WorkID:               req.Msg.WorkId,
			Version:              nextVersion,
			Title:                cloneOptionalString(source.Title),
			Summary:              cloneOptionalString(source.Summary),
			ContentSnapshot:      encoded,
			ContributorMemberIDs: contributors,
			CreatedAt:            time.Now().UTC(),
		}
		if createErr := tx.WithContext(ctx).Create(created).Error; createErr != nil {
			return createErr
		}
		if s.auditWriter != nil {
			if auditErr := domainaudit.AppendVersion(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditWorkUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewWorkVersionCreatedAuditRecord(
						metadata,
						req.Msg.WorkId,
						created.ID,
						created.ContributorMemberIDs,
					)
				},
			); auditErr != nil {
				return auditErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, normalizeWorkContentBlockError(err)
	}
	response := &intrav1.CreateWorkVersionCheckpointResponse{
		Created:  created != nil,
		Revision: revision,
		Locale:   locale,
	}
	if created != nil {
		response.VersionId = &created.ID
	}
	return connect.NewResponse(response), nil
}

func sameWorkVersionContentSnapshot(current []byte, desired []byte) (bool, error) {
	if len(current) == 0 {
		return false, nil
	}
	currentEnvelope, currentDocument, err := DecodeVersionContentSnapshot(current)
	if err != nil {
		return false, fmt.Errorf("decode previous Work version snapshot: %w", err)
	}
	desiredEnvelope, desiredDocument, err := DecodeVersionContentSnapshot(desired)
	if err != nil {
		return false, fmt.Errorf("decode desired Work version snapshot: %w", err)
	}
	return currentEnvelope.SchemaVersion == desiredEnvelope.SchemaVersion &&
		currentEnvelope.SourceLocale == desiredEnvelope.SourceLocale &&
		nullableStringEqual(currentEnvelope.Title, desiredEnvelope.Title) &&
		nullableStringEqual(currentEnvelope.Summary, desiredEnvelope.Summary) &&
		proto.Equal(currentDocument, desiredDocument), nil
}
