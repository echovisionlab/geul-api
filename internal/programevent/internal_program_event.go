package programevent

import (
	"context"
	"strings"
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
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// InternalProgramEventService is the durable typed Block boundary. Yjs state
// remains resident in editor-collab and never crosses this service.
type InternalProgramEventService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	auditWriter    domainaudit.Appender
	spiceDB        *auth.SpiceDBClient
	checkpoints    persistencecheckpoint.ContributorFence
	contentBlocks  *contentblock.Store
	mediaHydrator  ContentBlockMediaHydrator
}

func NewAuditedInternalProgramEventService(db *gorm.DB, asyncPublisher AsyncPublisher, auditWriter domainaudit.Appender, options ...InternalProgramEventServiceOption) *InternalProgramEventService {
	if auditWriter == nil {
		panic("internal program event audit writer is required")
	}
	service := NewInternalProgramEventService(db, asyncPublisher, options...)
	service.auditWriter = auditWriter
	return service
}

type InternalProgramEventServiceOption func(*InternalProgramEventService)

func WithInternalProgramEventSpiceDB(spiceDB *auth.SpiceDBClient) InternalProgramEventServiceOption {
	return func(service *InternalProgramEventService) { service.spiceDB = spiceDB }
}

func WithInternalProgramEventCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalProgramEventServiceOption {
	return func(service *InternalProgramEventService) { service.checkpoints = checkpoints }
}

func WithInternalProgramEventContentBlockStore(store *contentblock.Store) InternalProgramEventServiceOption {
	return func(service *InternalProgramEventService) { service.contentBlocks = store }
}

func WithInternalProgramEventMediaHydrator(hydrator ContentBlockMediaHydrator) InternalProgramEventServiceOption {
	return func(service *InternalProgramEventService) { service.mediaHydrator = hydrator }
}

func NewInternalProgramEventService(db *gorm.DB, asyncPublisher AsyncPublisher, options ...InternalProgramEventServiceOption) *InternalProgramEventService {
	if db == nil {
		panic("InternalProgramEventService: db is required")
	}
	service := &InternalProgramEventService{db: db, asyncPublisher: asyncPublisher}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalProgramEventService) LoadProgramEventBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadProgramEventBlockDocumentRequest],
) (*connect.Response[intrav1.LoadProgramEventBlockDocumentResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	if s.spiceDB == nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if s.mediaHydrator == nil {
		return nil, errs.InternalMsg("Program Event Block media hydrator is not configured")
	}
	locale, err := normalizeProgramEventDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	var localeState programEventExactLocaleState
	var sourceState programEventSourceState
	var sourceMetadata *intrav1.ProgramEventLocaleMetadata
	var blockMedia []*contentv1.ContentBlockMediaItem
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, principal, err := requireProgramEventBlockBootstrap(ctx, tx, s.spiceDB, req.Msg.EventId, req.Msg.Principal)
		if err != nil {
			return err
		}
		sourceState, err = loadProgramEventSourceLocale(ctx, tx, req.Msg.EventId, false)
		if err != nil {
			return err
		}
		localeState, err = loadProgramEventExactLocaleState(
			ctx, tx, s.contentBlocks, req.Msg.EventId, documentID, locale, false,
		)
		if err != nil {
			return err
		}
		sourceMetadata, err = loadProgramEventBlockLocaleMetadata(ctx, tx, req.Msg.EventId, sourceState.SourceLocale)
		if err != nil {
			return err
		}
		blockMedia, err = LoadContentBlockMediaReferences(ctx, tx, documentID)
		if err != nil {
			return err
		}
		blockMedia, err = s.mediaHydrator.HydrateAuthorizedProgramEventBlockMediaWithDB(
			auth.WithUser(ctx, principal), tx, req.Msg.EventId, documentID, principal, blockMedia,
		)
		return err
	}); err != nil {
		return nil, err
	}
	document, err := materializeProgramEventExactLocaleDocument(localeState, locale)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(localeState.Snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	var localeMetadata *intrav1.ProgramEventLocaleMetadata
	if localeState.TargetMetadata != nil {
		localeMetadata = programEventLocaleMetadataProjection(
			sourceMetadata, localeState.TargetMetadata, locale,
		)
	}
	var targetRevision *string
	if locale != localeState.SourceLocale && localeState.TargetMetadata != nil {
		targetRevision = &localeState.TargetRevision
	}
	return connect.NewResponse(&intrav1.LoadProgramEventBlockDocumentResponse{
		Document: document, DocumentRevision: localeState.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata, BlockMedia: blockMedia, Locale: locale,
		LocaleExists: localeState.TargetMetadata != nil, LocaleMetadata: localeMetadata,
		TargetRevision:      targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalProgramEventService) ApplyProgramEventBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyProgramEventBlockBatchRequest],
) (*connect.Response[intrav1.ApplyProgramEventBlockBatchResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	if req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	locale, err := normalizeProgramEventDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, s.db, req.Msg.EventId, false)
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
	batch, err := contentblock.BatchFromRichTextStorage(documentID, req.Msg.Batch.Profile, storage)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	for index := range batch.LocaleGroups {
		normalized, normalizeErr := normalizeProgramEventDocumentLocale(batch.LocaleGroups[index].Locale)
		if normalizeErr != nil {
			return nil, errs.InvalidArgument("locale", "unsupported Block locale")
		}
		if normalized != locale {
			return nil, errs.InvalidArgument("locale", "Block mutation must match the authenticated room locale")
		}
		batch.LocaleGroups[index].Locale = normalized
	}
	contributors := req.Msg.GetBatch().GetContributorMemberIds()
	if len(contributors) != 1 {
		return nil, errs.InvalidArgument(
			"contributor_member_ids", "collaboration mutation requires exactly one origin Member",
		)
	}
	originMemberID := contributors[0]
	now := time.Now().UTC()
	var result contentblock.Result
	var sourceState programEventSourceState
	sourceState, err = loadProgramEventSourceLocale(ctx, s.db, req.Msg.EventId, false)
	if err != nil {
		return nil, err
	}
	fence := programEventContentDocumentFence(req.Msg.EventId, func(ctx context.Context, tx *gorm.DB) error {
		if err := RequireExists(ctx, tx, req.Msg.EventId); err != nil {
			return err
		}
		return requireProgramEventContentContributors(ctx, tx, s.checkpoints, req.Msg.EventId, contributors)
	})
	if locale != sourceState.SourceLocale {
		var targetRevision string
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			output, applyErr := applyProgramEventTargetMutation(
				ctx, tx, s.contentBlocks,
				programEventTargetMutationInput{
					EventID: req.Msg.EventId, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, Now: now, Fence: fence,
				},
			)
			if applyErr != nil {
				return applyErr
			}
			result, targetRevision = output.Result, output.TargetRevision
			if result.Changed {
				return appendProgramEventMemberLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, req.Msg.EventId, locale,
					programEventTargetLocaleContentOperation(false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if result.Changed {
			targetRevisionValue := targetRevision
			publishContentUpdatedEvent(ctx, s.asyncPublisher, buildProgramEventBlockContentUpdatedEvent(
				req.Msg.EventId,
				[]string{"content"},
				result.DocumentRevision.String(),
				contributors,
				locale,
				true,
				&targetRevisionValue,
				false,
			))
		}
		return connect.NewResponse(&intrav1.ApplyProgramEventBlockBatchResponse{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: false, ChangedLocales: result.ChangedLocales,
			Locale: locale, TargetRevision: &targetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result, err = s.contentBlocks.ApplyBatch(
			ctx, tx, batch, fence,
		)
		if err != nil {
			return normalizeProgramEventContentBlockError(err)
		}
		if result.TranslationSourceChanged {
			// The owning Program Event root and content document revision are the
			// complete source authority. No separate translation epoch is stored.
		}
		if !result.Changed {
			return nil
		}
		if err := tx.Model(&model.ProgramEvent{}).
			Where("id = ?", req.Msg.EventId).
			Update("updated_at", now).Error; err != nil {
			return err
		}
		return appendProgramEventMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, originMemberID, req.Msg.EventId, locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	}); err != nil {
		return nil, err
	}
	if result.Changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildProgramEventBlockContentUpdatedEvent(
			req.Msg.EventId,
			[]string{"content"},
			result.DocumentRevision.String(),
			contributors,
			locale,
			true,
			nil,
			true,
		))
	}
	return connect.NewResponse(&intrav1.ApplyProgramEventBlockBatchResponse{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged:  result.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales,
		Locale:         locale,
	}), nil
}

func (s *InternalProgramEventService) UpdateProgramEventLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateProgramEventLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateProgramEventLocaleMetadataResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	locale, err := normalizeProgramEventDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	expectedRevision, err := uuidutil.ParseCanonical(req.Msg.ExpectedRevision, "expected_revision")
	if err != nil {
		return nil, errs.InvalidArgument("expected_revision", err.Error())
	}
	patchTitle := req.Msg.Title != nil
	patchSummary := req.Msg.Summary != nil
	if !patchTitle && !patchSummary {
		return nil, errs.InvalidArgument("metadata", "title or summary mutation is required")
	}
	if len(req.Msg.ContributorMemberIds) != 1 {
		return nil, errs.InvalidArgument(
			"contributor_member_ids", "collaboration mutation requires exactly one origin Member",
		)
	}
	originMemberID := req.Msg.ContributorMemberIds[0]
	summary, err := programEventSummaryMutationValue(req.Msg.Summary)
	if err != nil {
		return nil, err
	}
	documentID, err := loadProgramEventContentDocumentID(ctx, s.db, req.Msg.EventId, false)
	if err != nil {
		return nil, err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, s.db, req.Msg.EventId, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if locale != sourceState.SourceLocale {
		if patchTitle {
			return nil, errs.InvalidArgument("title", "Program Event title is source-owned")
		}
		contributors := make([]uuid.UUID, len(req.Msg.ContributorMemberIds))
		for index, raw := range req.Msg.ContributorMemberIds {
			contributors[index], err = uuidutil.ParseCanonical(raw, "contributor_member_ids")
			if err != nil {
				return nil, errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
			}
		}
		batch := contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: expectedRevision,
			ContributorMemberIDs: contributors,
		}
		fence := programEventContentDocumentFence(req.Msg.EventId, func(ctx context.Context, tx *gorm.DB) error {
			if err := RequireExists(ctx, tx, req.Msg.EventId); err != nil {
				return err
			}
			return requireProgramEventContentContributors(
				ctx, tx, s.checkpoints, req.Msg.EventId, req.Msg.ContributorMemberIds,
			)
		})
		var output programEventTargetMutationResult
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var applyErr error
			output, applyErr = applyProgramEventTargetMutation(
				ctx, tx, s.contentBlocks,
				programEventTargetMutationInput{
					EventID: req.Msg.EventId, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: expectedRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, SetSummary: patchSummary, Summary: summary,
					Now: now, Fence: fence,
				},
			)
			if applyErr != nil {
				return applyErr
			}
			if output.Result.Changed {
				return appendProgramEventMemberLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, req.Msg.EventId, locale,
					programEventTargetLocaleContentOperation(false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if output.Result.Changed {
			publishContentUpdatedEvent(ctx, s.asyncPublisher, buildProgramEventBlockContentUpdatedEvent(
				req.Msg.EventId,
				[]string{"summary"},
				output.Result.DocumentRevision.String(),
				[]string{originMemberID},
				locale,
				true,
				&output.TargetRevision,
				false,
			))
		}
		return connect.NewResponse(&intrav1.UpdateProgramEventLocaleMetadataResponse{
			DocumentRevision: output.Result.DocumentRevision.String(), Changed: output.Result.Changed,
			SourceChanged: false, ChangedLocales: output.Result.ChangedLocales,
			Locale:         locale,
			TargetRevision: &output.TargetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	var advanced contentblock.AdvanceResult
	var sourceLocale string
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		advanced, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			programEventContentDocumentFence(req.Msg.EventId, func(ctx context.Context, tx *gorm.DB) error {
				if err := RequireExists(ctx, tx, req.Msg.EventId); err != nil {
					return err
				}
				return requireProgramEventContentContributors(ctx, tx, s.checkpoints, req.Msg.EventId, req.Msg.ContributorMemberIds)
			}),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				current, loadErr := loadProgramEventSourceLocale(ctx, tx, req.Msg.EventId, true)
				if loadErr != nil {
					return contentblock.MetadataEffect{}, loadErr
				}
				sourceLocale = current.SourceLocale
				changed, affectsSource, state, err := applyProgramEventLocaleMetadataMutation(
					ctx, tx, req.Msg.EventId, sourceLocale, req.Msg.Title, patchTitle, summary, patchSummary, now,
				)
				sourceState = state
				return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: affectsSource}, err
			},
		)
		if err != nil {
			return normalizeProgramEventContentBlockError(err)
		}
		if !advanced.Changed {
			return nil
		}
		return appendProgramEventMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, originMemberID, req.Msg.EventId, locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	}); err != nil {
		return nil, err
	}
	if advanced.Changed {
		paths := make([]string, 0, 2)
		if patchTitle {
			paths = append(paths, "title")
		}
		if patchSummary {
			paths = append(paths, "summary")
		}
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildProgramEventBlockContentUpdatedEvent(
			req.Msg.EventId,
			paths,
			advanced.DocumentRevision.String(),
			req.Msg.ContributorMemberIds,
			locale,
			true,
			nil,
			true,
		))
	}
	return connect.NewResponse(&intrav1.UpdateProgramEventLocaleMetadataResponse{
		DocumentRevision: advanced.DocumentRevision.String(), Changed: advanced.Changed,
		SourceChanged:  advanced.TranslationSourceChanged,
		ChangedLocales: []string{sourceLocale},
		Locale:         locale,
	}), nil
}

func applyProgramEventLocaleMetadataMutation(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	locale string,
	title *string,
	patchTitle bool,
	summary *string,
	patchSummary bool,
	now time.Time,
) (bool, bool, programEventSourceState, error) {
	var root model.ProgramEvent
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "title", "source_locale").
		Where("id = ?", eventID).
		Take(&root).Error; err != nil {
		return false, false, programEventSourceState{}, err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, tx, eventID, true)
	if err != nil {
		return false, false, sourceState, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return false, false, sourceState, errs.FailedPrecondition("Program Event source locale changed; reload before saving")
	}
	if locale != sourceState.SourceLocale {
		return false, false, sourceState, errs.FailedPrecondition("target Program Event locale metadata is read-only")
	}
	var localeRow model.ProgramEventTranslation
	loadLocaleRow := func() error {
		return tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("entity_id = ? AND locale = ?", eventID, locale).
			Take(&localeRow).Error
	}
	if err := loadLocaleRow(); err != nil {
		return false, false, sourceState, err
	}
	changed := false
	if patchTitle {
		if title == nil || strings.TrimSpace(*title) == "" {
			return false, false, sourceState, errs.Required("title")
		}
		normalized := strings.TrimSpace(*title)
		if normalized != root.Title {
			if err := tx.Model(&model.ProgramEvent{}).
				Where("id = ?", eventID).
				Updates(structured.Fields{"title": normalized, "updated_at": now}).Error; err != nil {
				return false, false, sourceState, errs.Internal(err)
			}
			changed = true
		}
	}
	if patchSummary && !sameNullableString(localeRow.Summary, summary) {
		updates := structured.Fields{
			"summary":    summary,
			"updated_at": now,
		}
		if err := tx.Model(&model.ProgramEventTranslation{}).
			Where("entity_id = ? AND locale = ?", eventID, locale).
			Updates(updates).Error; err != nil {
			return false, false, sourceState, errs.Internal(err)
		}
		changed = true
	}
	return changed, changed && locale == sourceState.SourceLocale, sourceState, nil
}

func programEventSummaryMutationValue(input *intrav1.NullableStringMutation) (*string, error) {
	if input == nil {
		return nil, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := operation.Set
		return &value, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return nil, errs.InvalidArgument("summary", "clear must be true")
		}
		return nil, nil
	default:
		return nil, errs.InvalidArgument("summary", "set or clear is required")
	}
}
