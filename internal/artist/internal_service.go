package artist

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
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
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func validateArtistParentTransition(ctx context.Context, tx *gorm.DB, artistID, parentID string) error {
	if err := lockArtistParentGraph(ctx, tx); err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	if parentID == artistID {
		return errs.InvalidArgument("parent_artist_id", "cannot reference the same artist")
	}
	if !IsValidUUID(parentID) {
		return errs.InvalidArgument("parent_artist_id", "must be a valid Artist ID")
	}
	var parentExists bool
	if err := tx.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM artist WHERE id = ?::uuid)`, parentID).Scan(&parentExists).Error; err != nil {
		return err
	}
	if !parentExists {
		return errs.InvalidArgument("parent_artist_id", "artist does not exist")
	}
	var cycle bool
	if err := tx.WithContext(ctx).Raw(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM artist WHERE parent_artist_id = ?::uuid
			UNION
			SELECT child.id
			FROM artist child
			JOIN descendants parent ON child.parent_artist_id = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?::uuid)
	`, artistID, parentID).Scan(&cycle).Error; err != nil {
		return err
	}
	if cycle {
		return errs.InvalidArgument("parent_artist_id", "would create an artist hierarchy cycle")
	}
	return nil
}

func lockArtistParentGraph(ctx context.Context, tx *gorm.DB) error {
	return tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtext('artist_parent_graph'))").
		Error
}

type InternalArtistService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	spiceDB        *auth.SpiceDBClient
	checkpoints    persistencecheckpoint.ContributorFence
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	runtime        Runtime
	translation    Translation
}

type InternalArtistServiceOption func(*InternalArtistService)

func WithInternalArtistContentBlockStore(store *contentblock.Store) InternalArtistServiceOption {
	return func(service *InternalArtistService) { service.contentBlocks = store }
}

func WithInternalArtistCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalArtistServiceOption {
	return func(service *InternalArtistService) { service.checkpoints = checkpoints }
}

func NewAuditedInternalArtistService(db *gorm.DB, asyncPublisher AsyncPublisher, spiceDB *auth.SpiceDBClient, auditWriter domainaudit.Appender, dependencies Dependencies, options ...InternalArtistServiceOption) *InternalArtistService {
	if auditWriter == nil {
		panic("internal artist audit writer is required")
	}
	service := NewInternalArtistService(db, asyncPublisher, spiceDB, dependencies, options...)
	service.auditWriter = auditWriter
	return service
}

func NewInternalArtistService(db *gorm.DB, asyncPublisher AsyncPublisher, spiceDB *auth.SpiceDBClient, dependencies Dependencies, options ...InternalArtistServiceOption) *InternalArtistService {
	if db == nil {
		panic("InternalArtistService: db is required")
	}
	if asyncPublisher == nil {
		panic("InternalArtistService: asyncPublisher is required")
	}
	if spiceDB == nil {
		panic("InternalArtistService: spiceDB is required")
	}
	if dependencies.Runtime == nil {
		panic("InternalArtistService: runtime is required")
	}
	if dependencies.Translation == nil {
		panic("InternalArtistService: translation is required")
	}
	service := &InternalArtistService{
		db: db, asyncPublisher: asyncPublisher, spiceDB: spiceDB, runtime: dependencies.Runtime, translation: dependencies.Translation,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalArtistService) LoadArtistBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadArtistBlockDocumentRequest],
) (*connect.Response[intrav1.LoadArtistBlockDocumentResponse], error) {
	locale, err := normalizeArtistDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	var localeState artistTargetLocaleState
	var metadata *intrav1.ArtistDocumentMetadata
	bootstrap, err := loadCreativeContentBlockBootstrap(
		ctx, s.db, s.contentBlocks, s.spiceDB, artistContentEntity, req.Msg.ArtistId,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_ARTIST,
		req.Msg.Principal,
		func(ctx context.Context, tx *gorm.DB) error {
			var loadErr error
			documentID, loadErr := loadCreativeContentDocumentID(ctx, tx, artistContentEntity, req.Msg.ArtistId)
			if loadErr != nil {
				return loadErr
			}
			localeState, loadErr = loadArtistTargetLocaleState(
				ctx, tx, s.contentBlocks, req.Msg.ArtistId, documentID, locale, false,
			)
			if loadErr != nil {
				return loadErr
			}
			metadata, loadErr = loadArtistDocumentMetadata(ctx, tx, req.Msg.ArtistId)
			return loadErr
		},
	)
	if err != nil {
		return nil, err
	}
	document, err := artistLocalizedDocument(localeState, locale)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(bootstrap.Snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	sourceMetadata := &intrav1.ArtistLocaleMetadata{
		Locale: localeState.SourceLocale, Title: localeState.SourceMetadata.Title,
	}
	var localeMetadata *intrav1.ArtistLocaleMetadata
	if localeState.TargetMetadata != nil {
		localeMetadata = &intrav1.ArtistLocaleMetadata{
			Locale: locale, Title: localeState.TargetMetadata.Title,
		}
	}
	var targetRevision *string
	if locale != localeState.SourceLocale && localeState.TargetMetadata != nil {
		targetRevision = &localeState.TargetRevision
	}
	return connect.NewResponse(&intrav1.LoadArtistBlockDocumentResponse{
		Document: document, DocumentRevision: bootstrap.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata, Metadata: metadata, Locale: locale,
		LocaleExists: localeState.TargetMetadata != nil, LocaleMetadata: localeMetadata,
		TargetRevision: targetRevision, PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalArtistService) ApplyArtistBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyArtistBlockBatchRequest],
) (*connect.Response[intrav1.ApplyArtistBlockBatchResponse], error) {
	if s.contentBlocks == nil || req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	locale, err := normalizeArtistDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadCreativeContentDocumentID(ctx, s.db, artistContentEntity, req.Msg.ArtistId)
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
	expectedDocumentRevision, err := uuid.Parse(req.Msg.Batch.ExpectedRevision)
	if err != nil || expectedDocumentRevision == uuid.Nil {
		return nil, errs.InvalidArgument("batch.expected_revision", "must be a UUID")
	}
	now := time.Now().UTC()
	var result contentblock.Result
	var targetRevision string
	var sourceLocale string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := internalCreativeContentFence(artistContentEntity, req.Msg.ArtistId)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		sourceLocale = domain.SourceLocale
		if err := s.translation.RequireDocumentContributors(ctx, tx, req.Msg.Batch.ContributorMemberIds); err != nil {
			return err
		}
		if err := requireArtistCollaborationContributors(ctx, tx, s.checkpoints, req.Msg.ArtistId, req.Msg.Batch.ContributorMemberIds); err != nil {
			return err
		}
		fence := artistSourceRoomFence(req.Msg.ArtistId, sourceLocale)
		if locale != sourceLocale {
			batch, batchErr := contentblock.BatchFromRichTextStorage(documentID, req.Msg.Batch.Profile, storage)
			if batchErr != nil {
				return normalizeCreativeContentBlockError(artistContentEntity, batchErr)
			}
			result, targetRevision, err = applyArtistTargetLocaleBatch(
				ctx, tx, s.contentBlocks, req.Msg.ArtistId, documentID, locale, batch,
				req.Msg.ExpectedTargetRevision, false, now,
				internalCreativeContentFence(artistContentEntity, req.Msg.ArtistId),
			)
			if err != nil {
				return err
			}
			if result.Changed {
				return appendArtistMemberTargetLocaleAudit(
					ctx, tx, s.auditWriter, req.Msg.Batch.ContributorMemberIds[0],
					req.Msg.ArtistId, locale, sharedtelemetry.AuditItemOperationUpdated,
				)
			}
			return nil
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Artist room cannot carry a target revision")
		}
		if err := validateArtistSourceStorage(storage, sourceLocale); err != nil {
			return err
		}
		batch, batchErr := contentblock.BatchFromRichTextStorage(documentID, req.Msg.Batch.Profile, storage)
		if batchErr != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, batchErr)
		}
		result, err = s.contentBlocks.ApplyBatch(ctx, tx, batch, fence)
		if err != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, err)
		}
		if result.Changed {
			if err := tx.WithContext(ctx).Table("artist").Where("id = ?", req.Msg.ArtistId).UpdateColumn("updated_at", now).Error; err != nil {
				return err
			}
			if err := appendArtistMemberTargetLocaleAudit(
				ctx, tx, s.auditWriter, req.Msg.Batch.ContributorMemberIds[0],
				req.Msg.ArtistId, sourceLocale, sharedtelemetry.AuditItemOperationUpdated,
			); err != nil {
				return err
			}
		}
		if result.TranslationSourceChanged {
			_, err = s.runtime.RequestCurrentWithDB(
				ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
				req.Msg.ArtistId, "", false, "artist_source_document_saved",
			)
		}
		return err
	})
	if err != nil {
		return nil, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if result.Changed {
		var eventTargetRevision *string
		if locale != sourceLocale {
			eventTargetRevision = &targetRevision
		}
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildArtistContentUpdatedEvent(
			req.Msg.ArtistId,
			[]string{"content"},
			result.DocumentRevision.String(),
			req.Msg.Batch.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			eventTargetRevision,
			locale == sourceLocale,
		))
	}
	response := &intrav1.ApplyArtistBlockBatchResponse{Locale: locale}
	if locale == sourceLocale {
		response.DocumentRevision = result.DocumentRevision.String()
		response.Changed = result.Changed
		response.SourceChanged = result.TranslationSourceChanged
		response.ChangedLocales = result.ChangedLocales
	} else {
		response.DocumentRevision = result.DocumentRevision.String()
		response.Changed = result.Changed
		response.SourceChanged = false
		response.ChangedLocales = result.ChangedLocales
		response.TargetRevision = &targetRevision
	}
	return connect.NewResponse(response), nil
}

func (s *InternalArtistService) UpdateArtistLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateArtistLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateArtistLocaleMetadataResponse], error) {
	locale, err := normalizeArtistDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	sourceAuthority, err := loadCreativeDocumentAuthority(ctx, s.db, artistContentEntity, req.Msg.ArtistId)
	if err != nil {
		return nil, err
	}
	if locale != sourceAuthority.SourceLocale {
		expectedRevision, parseErr := uuid.Parse(req.Msg.ExpectedRevision)
		if parseErr != nil {
			return nil, errs.InvalidArgument("expected_revision", "must be a UUID")
		}
		documentID, loadErr := loadCreativeContentDocumentID(ctx, s.db, artistContentEntity, req.Msg.ArtistId)
		if loadErr != nil {
			return nil, loadErr
		}
		var result contentblock.Result
		var targetRevision string
		updateErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.translation.RequireDocumentContributors(ctx, tx, req.Msg.ContributorMemberIds); err != nil {
				return err
			}
			if err := requireArtistCollaborationContributors(ctx, tx, s.checkpoints, req.Msg.ArtistId, req.Msg.ContributorMemberIds); err != nil {
				return err
			}
			result, targetRevision, err = updateArtistTargetLocaleTitle(
				ctx, tx, s.contentBlocks, req.Msg.ArtistId, documentID, locale,
				expectedRevision, req.Msg.ExpectedTargetRevision, req.Msg.Title, time.Now().UTC(),
				internalCreativeContentFence(artistContentEntity, req.Msg.ArtistId),
			)
			return err
		})
		if updateErr != nil {
			return nil, updateErr
		}
		if result.Changed {
			_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildArtistContentUpdatedEvent(
				req.Msg.ArtistId,
				[]string{"title"},
				result.DocumentRevision.String(),
				req.Msg.ContributorMemberIds,
				managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				locale,
				true,
				&targetRevision,
				false,
			))
		}
		return connect.NewResponse(&intrav1.UpdateArtistLocaleMetadataResponse{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: false, ChangedLocales: result.ChangedLocales,
			Locale: locale, TargetRevision: &targetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	result, err := updateCreativeLocaleTitle(
		ctx,
		s.db,
		s.contentBlocks,
		s.runtime,
		artistContentEntity,
		req.Msg.ArtistId,
		locale,
		req.Msg.Title,
		req.Msg.ExpectedRevision,
		req.Msg.ContributorMemberIds,
		managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
		s.translation,
		s.checkpoints,
	)
	if err != nil {
		return nil, err
	}
	if result.Advance.Changed {
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildArtistContentUpdatedEvent(
			req.Msg.ArtistId,
			[]string{"title"},
			result.Advance.DocumentRevision.String(),
			req.Msg.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			nil,
			true,
		))
	}
	return connect.NewResponse(&intrav1.UpdateArtistLocaleMetadataResponse{
		DocumentRevision: result.Advance.DocumentRevision.String(), Changed: result.Advance.Changed,
		SourceChanged:  result.Advance.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales,
		Locale:         locale,
	}), nil
}

func loadArtistDocumentMetadata(
	ctx context.Context,
	db *gorm.DB,
	artistID string,
) (*intrav1.ArtistDocumentMetadata, error) {
	var artist model.Artist
	if err := db.WithContext(ctx).
		Select("id", "real_name", "country_code", "website", "social_links", "slug", "parent_artist_id").
		Where("id = ?", artistID).
		Take(&artist).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("artist", artistID)
		}
		return nil, errs.Internal(err)
	}
	var labelIDs []string
	if err := db.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("label_id ASC").
		Scan(&labelIDs).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return &intrav1.ArtistDocumentMetadata{
		RealName:       artist.RealName,
		CountryCode:    artist.CountryCode,
		Website:        artist.Website,
		SocialLinks:    artist.SocialLinks,
		Slug:           artist.Slug,
		LabelIds:       labelIDs,
		ParentArtistId: artist.ParentArtistID,
	}, nil
}
