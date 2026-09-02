package post

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// InternalPostService is the typed durable boundary used by editor-collab.
// Yjs runtime state never crosses or persists through this service.
type InternalPostService struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	asyncPublisher AsyncPublisher
	cdnDomain      string
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	ogRefresher    *og.Refresher
	mediaLoader    ContentBlockMediaLoader
}

type InternalPostServiceOption func(*InternalPostService)

func WithInternalPostDomainAuditWriter(writer domainaudit.Appender) InternalPostServiceOption {
	return func(s *InternalPostService) { s.auditWriter = writer }
}

func WithInternalPostContentBlockStore(store *contentblock.Store) InternalPostServiceOption {
	return func(s *InternalPostService) { s.contentBlocks = store }
}

func NewInternalPostService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	asyncPublisher AsyncPublisher,
	cdnDomain string,
	ogRefresher *og.Refresher,
	mediaLoader ContentBlockMediaLoader,
	options ...InternalPostServiceOption,
) *InternalPostService {
	if db == nil || spiceDB == nil || asyncPublisher == nil || mediaLoader == nil {
		panic("InternalPostService: DB, SpiceDB, async publisher, and media loader are required")
	}
	if ogRefresher == nil {
		panic("InternalPostService: OG refresher is required")
	}
	service := &InternalPostService{
		db: db, spiceDB: spiceDB, asyncPublisher: asyncPublisher, cdnDomain: cdnDomain, ogRefresher: ogRefresher,
		mediaLoader: mediaLoader,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalPostService) requireContentBlockStore() error {
	if s.contentBlocks == nil {
		return errs.InternalMsg("Post content Block store is not configured")
	}
	return nil
}

func (s *InternalPostService) LoadPostBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadPostBlockDocumentRequest],
) (*connect.Response[intrav1.LoadPostBlockDocumentResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.InvalidArgumentMsg("request is required")
	}
	locale, err := validatePostAIDocumentIdentity(req.Msg.PostId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, req.Msg.PostId)
	if err != nil {
		return nil, err
	}

	var snapshot contentblock.Snapshot
	var sourceLocale string
	var sourceMetadata postLocaleMetadataRow
	var localeMetadata *postLocaleMetadataRow
	var targetRevision *string
	var categoryIDs []string
	var tagIDs []string
	var mediaItems []*contentv1.ContentBlockMediaItem
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := authorizePostBlockBootstrap(
			ctx, tx, s.spiceDB, req.Msg.PostId, documentID, req.Msg.Principal,
		); err != nil {
			return err
		}
		domain, err := loadPostContentDomainContext(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		sourceLocale = domain.SourceLocale
		snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, sourceLocale,
		)
		if err != nil {
			return err
		}
		sourceMetadata, err = loadPostLocaleMetadataRow(ctx, tx, req.Msg.PostId, sourceLocale)
		if err != nil {
			return err
		}
		if locale == sourceLocale {
			projected := sourceMetadata
			localeMetadata = &projected
		} else {
			target, exists, loadErr := loadOptionalPostLocaleMetadataRow(
				ctx, tx, req.Msg.PostId, locale, false,
			)
			if loadErr != nil {
				return loadErr
			}
			if !exists && snapshotContainsLocale(snapshot, locale) {
				return errs.FailedPrecondition("Post target locale Blocks exist without owning metadata")
			}
			if exists {
				projected := postTargetMetadataProjection(sourceMetadata, &target, locale)
				localeMetadata = &projected
				revision, revisionErr := derivePostTargetRevision(snapshot.Document.Revision.String(), target)
				if revisionErr != nil {
					return revisionErr
				}
				targetRevision = &revision
			}
		}
		categoryIDs, tagIDs, err = loadPostTaxonomyIDs(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		mediaItems, err = s.mediaLoader.LoadContentBlockMediaReferences(ctx, tx, documentID)
		return err
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	document, err := postTargetRoomDocument(snapshot, locale)
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	sourceMetadataMessage := &intrav1.PostLocaleMetadata{
		Locale: sourceLocale, Title: sourceMetadata.Title, Summary: sourceMetadata.Summary,
	}
	var localeMetadataMessage *intrav1.PostLocaleMetadata
	if localeMetadata != nil {
		localeMetadataMessage = &intrav1.PostLocaleMetadata{
			Locale: localeMetadata.Locale, Title: localeMetadata.Title, Summary: localeMetadata.Summary,
		}
	}
	return connect.NewResponse(&intrav1.LoadPostBlockDocumentResponse{
		Document:            document,
		DocumentRevision:    snapshot.Document.Revision.String(),
		SourceMetadata:      sourceMetadataMessage,
		CategoryIds:         categoryIDs,
		TagIds:              tagIDs,
		BlockMedia:          mediaItems,
		Locale:              locale,
		LocaleExists:        localeMetadata != nil,
		LocaleMetadata:      localeMetadataMessage,
		TargetRevision:      targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalPostService) ApplyPostBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyPostBlockBatchRequest],
) (*connect.Response[intrav1.ApplyPostBlockBatchResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	locale, err := validatePostAIDocumentIdentity(req.Msg.PostId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.Batch.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, req.Msg.PostId)
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
	expectedDocumentRevision, err := parsePostContentUUID("batch.expected_revision", req.Msg.Batch.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var result contentblock.Result
	var targetResult postTargetLocaleMutationResult
	var sourceLocale string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := postCollaborationDocumentFence(
			s.spiceDB, req.Msg.PostId, req.Msg.Batch.ContributorMemberIds,
		)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		sourceLocale = domain.SourceLocale
		authorizedFence := postAuthorizedDocumentFence(documentID, domain)
		if locale != domain.SourceLocale {
			targetResult, err = applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
				PostID: req.Msg.PostId, Locale: locale,
				ExpectedDocumentRevision: expectedDocumentRevision,
				ExpectedTargetRevision:   req.Msg.ExpectedTargetRevision,
				Storage:                  &storage,
				Now:                      now,
				Fence:                    authorizedFence,
			})
			if err != nil {
				return err
			}
			if targetResult.TitleChanged {
				_, err = s.ogRefresher.RequestCurrentWithDB(
					ctx, tx,
					managev1.OgEntityType_OG_ENTITY_TYPE_POST,
					req.Msg.PostId, locale, false, "post_target_content_saved",
				)
				if err != nil {
					return err
				}
			}
			if !targetResult.Changed {
				return nil
			}
			operation := sharedtelemetry.AuditItemOperationUpdated
			if targetResult.LocaleCreated {
				operation = sharedtelemetry.AuditItemOperationCreated
			}
			return appendPostMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PostId, locale, operation,
			)
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Post room cannot carry a target revision")
		}
		if err := validatePostSourceStorage(storage, domain.SourceLocale); err != nil {
			return err
		}
		batch, batchErr := contentblock.BatchFromRichTextStorage(
			documentID,
			req.Msg.Batch.Profile,
			storage,
		)
		if batchErr != nil {
			return batchErr
		}
		result, err = s.contentBlocks.ApplyBatch(
			ctx,
			tx,
			batch,
			authorizedFence,
		)
		if err != nil {
			return err
		}
		if result.Changed {
			if err := tx.WithContext(ctx).Model(&model.Post{}).
				Where("id = ?", req.Msg.PostId).
				UpdateColumn("updated_at", now).Error; err != nil {
				return err
			}
			return appendPostMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PostId, locale,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		return nil
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	changed := result.Changed || targetResult.Changed
	documentRevision := result.DocumentRevision.String()
	if locale != sourceLocale {
		documentRevision = targetResult.DocumentRevision
	}
	if changed {
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildPostBlockContentUpdatedEvent(
			req.Msg.PostId,
			[]string{"content"},
			documentRevision,
			req.Msg.Batch.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			postTargetRevisionSignal(locale, sourceLocale, targetResult.TargetRevision),
			locale == sourceLocale,
		))
	}
	response := &intrav1.ApplyPostBlockBatchResponse{
		DocumentRevision: documentRevision, Changed: changed,
		SourceChanged:  result.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales,
		Locale:         locale,
	}
	if locale != sourceLocale {
		response.ChangedLocales = []string{locale}
		response.TargetRevision = &targetResult.TargetRevision
	}
	return connect.NewResponse(response), nil
}

func (s *InternalPostService) UpdatePostLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdatePostLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdatePostLocaleMetadataResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil || (req.Msg.TitleChange == nil && req.Msg.SummaryChange == nil) {
		return nil, errs.InvalidArgumentMsg("at least one typed locale metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(req.Msg.PostId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, req.Msg.PostId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var result contentblock.AdvanceResult
	var targetResult postTargetLocaleMutationResult
	var sourceLocale string
	var sourceMetadataChanged bool
	var titleChanged bool
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := postCollaborationDocumentFence(
			s.spiceDB, req.Msg.PostId, req.Msg.ContributorMemberIds,
		)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		sourceLocale = domain.SourceLocale
		authorizedFence := postAuthorizedDocumentFence(documentID, domain)
		if locale != domain.SourceLocale {
			title, titleSet, changeErr := postOptionalMetadataChangeValue(req.Msg.TitleChange)
			if changeErr != nil {
				return errs.InvalidArgument("title_change", changeErr.Error())
			}
			summary, summarySet, changeErr := postOptionalMetadataChangeValue(req.Msg.SummaryChange)
			if changeErr != nil {
				return errs.InvalidArgument("summary_change", changeErr.Error())
			}
			targetResult, err = applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
				PostID: req.Msg.PostId, Locale: locale,
				ExpectedDocumentRevision: expectedRevision,
				ExpectedTargetRevision:   req.Msg.ExpectedTargetRevision,
				SetTitle:                 titleSet,
				Title:                    title,
				SetSummary:               summarySet,
				Summary:                  summary,
				Now:                      now,
				Fence:                    authorizedFence,
			})
			if err != nil {
				return err
			}
			if targetResult.TitleChanged {
				_, err = s.ogRefresher.RequestCurrentWithDB(
					ctx, tx,
					managev1.OgEntityType_OG_ENTITY_TYPE_POST,
					req.Msg.PostId, locale, false, "post_target_metadata_saved",
				)
				if err != nil {
					return err
				}
			}
			if !targetResult.Changed {
				return nil
			}
			operation := sharedtelemetry.AuditItemOperationUpdated
			if targetResult.LocaleCreated {
				operation = sharedtelemetry.AuditItemOperationCreated
			}
			return appendPostMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PostId, locale, operation,
			)
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "source Post room cannot carry a target revision")
		}
		result, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			authorizedFence,
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				changed, changedTitle, mutationErr := updatePostLocaleMetadataRow(
					ctx,
					tx,
					req.Msg.PostId,
					locale,
					req.Msg.TitleChange,
					req.Msg.SummaryChange,
					sourceLocale,
					now,
				)
				titleChanged = changedTitle
				sourceMetadataChanged = changed
				return contentblock.MetadataEffect{
					Changed: changed, AffectsTranslationSource: sourceMetadataChanged,
				}, mutationErr
			},
		)
		if err != nil {
			return err
		}
		if result.Changed {
			if err := tx.WithContext(ctx).Model(&model.Post{}).
				Where("id = ?", req.Msg.PostId).
				UpdateColumn("updated_at", now).Error; err != nil {
				return err
			}
		}
		if !result.Changed {
			return nil
		}
		if titleChanged {
			_, err = s.ogRefresher.RequestCurrentWithDB(
				ctx, tx,
				managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				req.Msg.PostId, locale, false, "post_source_metadata_saved",
			)
			if err != nil {
				return err
			}
		}
		return appendPostMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PostId, locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	changed := result.Changed || targetResult.Changed
	documentRevision := result.DocumentRevision.String()
	if locale != sourceLocale {
		documentRevision = targetResult.DocumentRevision
	}
	if changed {
		fields := make([]string, 0, 2)
		if req.Msg.TitleChange != nil {
			fields = append(fields, "title")
		}
		if req.Msg.SummaryChange != nil {
			fields = append(fields, "summary")
		}
		_ = publishContentUpdatedEvent(ctx, s.asyncPublisher, buildPostBlockContentUpdatedEvent(
			req.Msg.PostId,
			fields,
			documentRevision,
			req.Msg.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			postTargetRevisionSignal(locale, sourceLocale, targetResult.TargetRevision),
			locale == sourceLocale,
		))
	}
	response := &intrav1.UpdatePostLocaleMetadataResponse{
		DocumentRevision: documentRevision, Changed: changed,
		SourceChanged: result.TranslationSourceChanged,
		Locale:        locale,
	}
	if changed {
		response.ChangedLocales = []string{locale}
	}
	if locale != sourceLocale {
		response.TargetRevision = &targetResult.TargetRevision
	}
	return connect.NewResponse(response), nil
}

func (s *InternalPostService) UpdatePostDocumentMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdatePostDocumentMetadataRequest],
) (*connect.Response[intrav1.UpdatePostDocumentMetadataResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil || (req.Msg.CategoryIds == nil && req.Msg.TagIds == nil) {
		return nil, errs.InvalidArgumentMsg("at least one typed document metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(req.Msg.PostId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, req.Msg.PostId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var result contentblock.AdvanceResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fence := func(ctx context.Context, tx *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
			domain, fenceErr := postCollaborationDocumentFence(
				s.spiceDB, req.Msg.PostId, req.Msg.ContributorMemberIds,
			)(ctx, tx, requestedDocumentID)
			if fenceErr != nil {
				return contentblock.DomainContext{}, fenceErr
			}
			if locale != domain.SourceLocale {
				return contentblock.DomainContext{}, errs.CollaborationMutationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
					"Post target locale cannot mutate shared document metadata",
				)
			}
			return domain, nil
		}
		result, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			fence,
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				currentCategories, currentTags, loadErr := loadPostTaxonomyIDs(ctx, tx, req.Msg.PostId)
				if loadErr != nil {
					return contentblock.MetadataEffect{}, loadErr
				}
				changed := false
				if req.Msg.CategoryIds != nil {
					next := normalizeStringIDs(req.Msg.CategoryIds.Ids)
					slices.Sort(next)
					if !slices.Equal(currentCategories, next) {
						if replaceErr := replacePostTaxonomyLinks(
							ctx, tx, "category", "post_category", "category_id", req.Msg.PostId, next,
						); replaceErr != nil {
							return contentblock.MetadataEffect{}, replaceErr
						}
						changed = true
					}
				}
				if req.Msg.TagIds != nil {
					next := normalizeStringIDs(req.Msg.TagIds.Ids)
					slices.Sort(next)
					if !slices.Equal(currentTags, next) {
						if replaceErr := replacePostTaxonomyLinks(
							ctx, tx, "tag", "post_tag", "tag_id", req.Msg.PostId, next,
						); replaceErr != nil {
							return contentblock.MetadataEffect{}, replaceErr
						}
						changed = true
					}
				}
				return contentblock.MetadataEffect{Changed: changed}, nil
			},
		)
		if err != nil {
			return err
		}
		if result.Changed {
			return tx.WithContext(ctx).Model(&model.Post{}).
				Where("id = ?", req.Msg.PostId).
				UpdateColumn("updated_at", now).Error
		}
		return nil
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	return connect.NewResponse(&intrav1.UpdatePostDocumentMetadataResponse{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged,
		Locale:        locale,
	}), nil
}

func (s *InternalPostService) CreatePostVersionCheckpoint(
	ctx context.Context,
	req *connect.Request[intrav1.CreatePostVersionCheckpointRequest],
) (*connect.Response[intrav1.CreatePostVersionCheckpointResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.InvalidArgumentMsg("request is required")
	}
	locale, err := validatePostAIDocumentIdentity(req.Msg.PostId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, req.Msg.PostId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	var created *model.PostVersion
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := postCollaborationDocumentFence(
			s.spiceDB, req.Msg.PostId, req.Msg.ContributorMemberIds,
		)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		snapshot, loadErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if loadErr != nil {
			return loadErr
		}
		if snapshot.Document.Revision != expectedRevision {
			return contentblock.ErrStaleRevision
		}
		if locale != domain.SourceLocale {
			return nil
		}
		metadata, metadataErr := loadPostLocaleMetadataRow(ctx, tx, req.Msg.PostId, domain.SourceLocale)
		if metadataErr != nil {
			return metadataErr
		}
		document, materializeErr := contentblock.SnapshotToLocalizedRichTextDocument(
			snapshot, domain.SourceLocale,
		)
		if materializeErr != nil {
			return materializeErr
		}
		encoded, _, encodeErr := marshalPostVersionContentSnapshot(
			document, domain.SourceLocale, metadata.Title, metadata.Summary,
		)
		if encodeErr != nil {
			return encodeErr
		}

		var last model.PostVersion
		lastResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("post_id = ?", req.Msg.PostId).
			Order("version DESC").
			First(&last)
		if lastResult.Error == nil && bytes.Equal(last.ContentSnapshot, encoded) {
			return nil
		}
		if lastResult.Error != nil && !errors.Is(lastResult.Error, gorm.ErrRecordNotFound) {
			return lastResult.Error
		}
		nextVersion := int32(1)
		if lastResult.Error == nil {
			nextVersion = last.Version + 1
		}
		version := &model.PostVersion{
			PostID: req.Msg.PostId, Version: nextVersion, ContentSnapshot: encoded,
			ContributorMemberIDs: normalizeContributorMemberIDs(req.Msg.ContributorMemberIds),
		}
		if err := tx.WithContext(ctx).Create(version).Error; err != nil {
			return err
		}
		created = version
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendVersion(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditPostUpdated,
			func(auditMetadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostVersionCreatedAuditRecord(
					auditMetadata, req.Msg.PostId, version.ID, version.ContributorMemberIDs,
				)
			},
		)
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	response := &intrav1.CreatePostVersionCheckpointResponse{
		Created: created != nil, Revision: expectedRevision.String(), Locale: locale,
	}
	if created != nil {
		response.VersionId = &created.ID
	}
	return connect.NewResponse(response), nil
}

type postLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func loadPostLocaleMetadataRow(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	locale string,
) (postLocaleMetadataRow, error) {
	var row postLocaleMetadataRow
	if err := db.WithContext(ctx).
		Table("post_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return postLocaleMetadataRow{}, errs.NotFound("post_translation", postID+":"+locale)
		}
		return postLocaleMetadataRow{}, err
	}
	return row, nil
}

func loadPostTaxonomyIDs(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) ([]string, []string, error) {
	var row struct {
		CategoryIDs pq.StringArray `gorm:"column:category_ids;type:text[]"`
		TagIDs      pq.StringArray `gorm:"column:tag_ids;type:text[]"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT ARRAY(
		          SELECT post_category.category_id::text
		          FROM post_category
		          WHERE post_category.post_id = ?::uuid
		          ORDER BY post_category.category_id
		        ) AS category_ids,
		        ARRAY(
		          SELECT post_tag.tag_id::text
		          FROM post_tag
		          WHERE post_tag.post_id = ?::uuid
		          ORDER BY post_tag.tag_id
		        ) AS tag_ids`,
		postID, postID,
	).Scan(&row)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	return []string(row.CategoryIDs), []string(row.TagIDs), nil
}

func replacePostTaxonomyLinks(
	ctx context.Context,
	tx *gorm.DB,
	targetTable string,
	junctionTable string,
	targetColumn string,
	postID string,
	rawIDs []string,
) error {
	ids := normalizeStringIDs(rawIDs)
	slices.Sort(ids)
	if len(ids) > 0 {
		var count int64
		if err := tx.WithContext(ctx).Table(targetTable).Where("id IN ?", ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return errs.InvalidArgumentMsg("one or more " + targetTable + " IDs do not exist")
		}
	}
	if err := tx.WithContext(ctx).Table(junctionTable).
		Where("post_id = ?", postID).
		Delete(nil).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	values := make([]structured.Fields, len(ids))
	for i, id := range ids {
		values[i] = structured.Fields{"post_id": postID, targetColumn: id}
	}
	return tx.WithContext(ctx).Table(junctionTable).Create(values).Error
}

func updatePostLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	titleChange *intrav1.PostNullableStringChange,
	summaryChange *intrav1.PostNullableStringChange,
	sourceLocale string,
	now time.Time,
) (bool, bool, error) {
	if strings.TrimSpace(sourceLocale) == "" {
		return false, false, errs.FailedPrecondition("Post source locale is not initialized")
	}
	if locale != sourceLocale {
		return false, false, errs.FailedPrecondition("target Post locale metadata is read-only")
	}
	if err := ensurePostLocaleMetadataRow(ctx, tx, postID, locale, sourceLocale, now); err != nil {
		return false, false, err
	}
	current, err := loadPostLocaleMetadataRow(
		ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), postID, locale,
	)
	if err != nil {
		return false, false, err
	}
	updates := structured.Fields{}
	titleChanged := false
	if titleChange != nil {
		next, changeErr := postNullableStringChangeValue(titleChange)
		if changeErr != nil {
			return false, false, errs.InvalidArgument("title_change", changeErr.Error())
		}
		if next != nil {
			trimmed := strings.TrimSpace(*next)
			if trimmed == "" {
				return false, false, errs.InvalidArgument("title_change", "title must not be empty")
			}
			next = &trimmed
		} else if locale == sourceLocale {
			return false, false, errs.InvalidArgument("title_change", "source title cannot be cleared")
		}
		if !nullableStringEqual(current.Title, next) {
			updates["title"] = next
			titleChanged = true
		}
	}
	if summaryChange != nil {
		next, changeErr := postNullableStringChangeValue(summaryChange)
		if changeErr != nil {
			return false, false, errs.InvalidArgument("summary_change", changeErr.Error())
		}
		if !nullableStringEqual(current.Summary, next) {
			updates["summary"] = next
		}
	}
	if len(updates) == 0 {
		return false, false, nil
	}
	updates["updated_at"] = now
	if err := tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Updates(updates).Error; err != nil {
		return false, false, err
	}
	return true, titleChanged, nil
}

func ensurePostLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	sourceLocale string,
	now time.Time,
) error {
	if locale != sourceLocale {
		return errs.FailedPrecondition("target Post locale metadata is read-only")
	}
	var count int64
	if err := tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errs.FailedPrecondition("Post source locale metadata is not initialized")
	}
	return nil
}

func postNullableStringChangeValue(change *intrav1.PostNullableStringChange) (*string, error) {
	if change == nil {
		return nil, errors.New("change is required")
	}
	switch value := change.Value.(type) {
	case *intrav1.PostNullableStringChange_SetValue:
		return &value.SetValue, nil
	case *intrav1.PostNullableStringChange_Clear:
		if !value.Clear {
			return nil, errors.New("clear must be true")
		}
		return nil, nil
	default:
		return nil, errors.New("set or clear is required")
	}
}

func postOptionalMetadataChangeValue(
	change *intrav1.PostNullableStringChange,
) (*string, bool, error) {
	if change == nil {
		return nil, false, nil
	}
	value, err := postNullableStringChangeValue(change)
	return value, true, err
}
