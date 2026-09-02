package page

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// InternalPageService is the typed durable boundary used by editor-collab.
// Yjs remains resident collaboration state and never crosses this API.
type InternalPageService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	auditWriter    domainaudit.Appender
	spiceDB        CollaborationPermissionChecker
	contentBlocks  *contentblock.Store
	mediaHydrator  ContentBlockMediaHydrator
	og             OGRequests
}

type InternalPageServiceOption func(*InternalPageService)

func WithInternalPageDomainAuditWriter(writer domainaudit.Appender) InternalPageServiceOption {
	return func(service *InternalPageService) {
		service.auditWriter = writer
	}
}

func WithInternalPageContentBlockStore(store *contentblock.Store) InternalPageServiceOption {
	return func(service *InternalPageService) {
		service.contentBlocks = store
	}
}

func WithInternalPageContentBlockMediaHydrator(
	hydrator ContentBlockMediaHydrator,
) InternalPageServiceOption {
	return func(service *InternalPageService) {
		service.mediaHydrator = hydrator
	}
}

func NewInternalPageService(
	db *gorm.DB,
	asyncPublisher AsyncPublisher,
	spiceDB CollaborationPermissionChecker,
	ogRequests OGRequests,
	options ...InternalPageServiceOption,
) *InternalPageService {
	if db == nil {
		panic("InternalPageService: db is required")
	}
	if asyncPublisher == nil {
		panic("InternalPageService: asyncPublisher is required")
	}
	if spiceDB == nil {
		panic("InternalPageService: SpiceDB checker is required")
	}
	if ogRequests == nil {
		panic("InternalPageService: OG requests are required")
	}
	service := &InternalPageService{
		db:             db,
		asyncPublisher: asyncPublisher,
		spiceDB:        spiceDB,
		og:             ogRequests,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalPageService) requireContentBlockStore() error {
	if s.contentBlocks == nil {
		return errs.InternalMsg("Page content Block store is not configured")
	}
	return nil
}

func (s *InternalPageService) LoadPageBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadPageBlockDocumentRequest],
) (*connect.Response[intrav1.LoadPageBlockDocumentResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if s.mediaHydrator == nil {
		return nil, errs.InternalMsg("Page content Block media hydrator is not configured")
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	if _, err := parsePageContentUUID("page_id", req.Msg.PageId); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}

	var state pageTargetLocaleState
	var documentLayout model.DocumentLayout
	var blockMedia []*contentv1.ContentBlockMediaItem
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, err := loadPageContentDocumentIDForRead(ctx, tx, req.Msg.PageId)
		if err != nil {
			return err
		}
		principal, err := authorizePageBlockBootstrap(
			ctx, tx, s.spiceDB, req.Msg.PageId, req.Msg.Principal,
		)
		if err != nil {
			return err
		}
		state, err = loadPageTargetLocaleState(ctx, tx, s.contentBlocks, req.Msg.PageId, documentID, locale, false)
		if err != nil {
			return err
		}
		var row struct {
			DocumentLayout model.DocumentLayout `gorm:"column:document_layout"`
		}
		if err := tx.WithContext(ctx).
			Table("page").
			Select("document_layout").
			Where("id = ?", req.Msg.PageId).
			Take(&row).Error; err != nil {
			return err
		}
		documentLayout = row.DocumentLayout
		blockMedia, err = LoadContentBlockMediaReferences(ctx, tx, documentID)
		if err != nil {
			return err
		}
		blockMedia, err = s.mediaHydrator.HydrateAuthorizedPageBlockMediaWithDB(
			auth.WithUser(ctx, principal), tx, req.Msg.PageId, documentID, principal, blockMedia,
		)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	document, err := contentblock.MaterializeSnapshotPageLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentPageLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	return connect.NewResponse(&intrav1.LoadPageBlockDocumentResponse{
		Document: document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.PageLocaleMetadata{Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary},
		Locale:         locale, LocaleExists: state.TargetMetadata != nil,
		DocumentLayout:      documentLayout.Proto(),
		BlockMedia:          blockMedia,
		LocaleMetadata:      pageLocaleMetadataProto(state.TargetMetadata),
		TargetRevision:      clonePageTargetRevision(state.TargetRevision),
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalPageService) ApplyPageBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyPageBlockBatchRequest],
) (*connect.Response[intrav1.ApplyPageBlockBatchResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	if _, err := parsePageContentUUID("page_id", req.Msg.PageId); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.Batch.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, req.Msg.PageId)
	if err != nil {
		return nil, err
	}
	batch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(
		documentID,
		req.Msg.Batch,
		locale,
		req.Msg.AffectedLocaleValues,
	)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	now := time.Now().UTC()
	var result contentblock.Result
	var targetRevision *string
	var sourcePath bool
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		baseFence := pageCollaborationDocumentFence(s.spiceDB, req.Msg.PageId, policyv1.Page.Edit, req.Msg.Batch.ContributorMemberIds)
		domain, fenceErr := baseFence(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		if locale == domain.SourceLocale {
			sourcePath = true
			if req.Msg.ExpectedTargetRevision != nil {
				return errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
			}
			result, err = s.contentBlocks.ApplyBatch(ctx, tx, batch, pageSourceRoomFence(req.Msg.PageId, locale, pageLockedTargetFence(documentID, domain)))
		} else {
			result, targetRevision, err = applyPageTargetLocaleBatch(
				ctx, tx, s.contentBlocks, req.Msg.PageId, documentID, locale, batch,
				req.Msg.ExpectedTargetRevision, pageTargetMetadataPatch{}, false, false, now,
				pageLockedTargetFence(documentID, domain),
			)
		}
		if err != nil {
			return err
		}
		if locale != domain.SourceLocale {
			if !result.Changed {
				return nil
			}
			if err := tx.WithContext(ctx).Model(&model.Page{}).Where("id = ?", req.Msg.PageId).UpdateColumn("updated_at", now).Error; err != nil {
				return err
			}
			return appendPageMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PageId, locale,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		if err := ValidateSourceLocaleChanges(
			ctx, tx, req.Msg.PageId, domain.SourceLocale, result.ChangedLocales,
		); err != nil {
			return err
		}
		if !result.Changed {
			return nil
		}
		if err := tx.WithContext(ctx).Model(&model.Page{}).
			Where("id = ?", req.Msg.PageId).
			UpdateColumn("updated_at", now).Error; err != nil {
			return err
		}
		return appendPageMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PageId, locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	if result.Changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildPageContentUpdatedEvent(
			req.Msg.PageId,
			[]string{"content"},
			result.DocumentRevision.String(),
			req.Msg.Batch.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			targetRevision,
			sourcePath,
		))
	}
	return connect.NewResponse(&intrav1.ApplyPageBlockBatchResponse{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged:  result.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales, Locale: locale,
		TargetRevision: targetRevision,
	}), nil
}

func pageLocaleMetadataProto(row *pageLocaleMetadataRow) *intrav1.PageLocaleMetadata {
	if row == nil {
		return nil
	}
	return &intrav1.PageLocaleMetadata{Locale: row.Locale, Title: cloneOptionalString(row.Title), Summary: cloneOptionalString(row.Summary)}
}

func clonePageTargetRevision(value string) *string {
	if value == "" {
		return nil
	}
	cloned := value
	return &cloned
}

type pageLocaleMetadataMutationPlan struct {
	Locale        string
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
}

type pageLocaleMetadataMutationResult struct {
	Effect         contentblock.MetadataEffect
	Title          *string
	Summary        *string
	TitleChanged   bool
	SummaryChanged bool
}

func planPageLocaleMetadataMutation(
	request *intrav1.UpdatePageLocaleMetadataRequest,
	locale string,
	isSource bool,
) (pageLocaleMetadataMutationPlan, error) {
	if request == nil {
		return pageLocaleMetadataMutationPlan{}, errs.Required("request")
	}
	plan := pageLocaleMetadataMutationPlan{Locale: locale}
	if request.Title != nil {
		title := *request.Title
		if isSource {
			title = strings.TrimSpace(title)
		}
		if isSource && title == "" {
			return pageLocaleMetadataMutationPlan{}, errs.InvalidArgument("title", "must not be empty")
		}
		plan.UpdateTitle = true
		plan.Title = &title
	}
	switch change := request.SummaryChange.(type) {
	case nil:
	case *intrav1.UpdatePageLocaleMetadataRequest_SetSummary:
		plan.UpdateSummary = true
		plan.Summary = cloneOptionalString(&change.SetSummary)
	case *intrav1.UpdatePageLocaleMetadataRequest_ClearSummary:
		plan.UpdateSummary = true
		plan.Summary = nil
	default:
		return pageLocaleMetadataMutationPlan{}, errs.InvalidArgument("summary_change", "is invalid")
	}
	if !plan.UpdateTitle && !plan.UpdateSummary {
		return pageLocaleMetadataMutationPlan{}, errs.InvalidArgument("metadata", "title or summary_change is required")
	}
	return plan, nil
}

func (s *InternalPageService) UpdatePageLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdatePageLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdatePageLocaleMetadataResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	if _, err := parsePageContentUUID("page_id", req.Msg.PageId); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(req.Msg.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, req.Msg.PageId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var advance contentblock.AdvanceResult
	var targetResult contentblock.Result
	var targetRevision *string
	var targetPath bool
	var mutation pageLocaleMetadataMutationResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		baseFence := pageCollaborationDocumentFence(s.spiceDB, req.Msg.PageId, policyv1.Page.Edit, req.Msg.ContributorMemberIds)
		domain, fenceErr := baseFence(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		plan, planErr := planPageLocaleMetadataMutation(req.Msg, locale, locale == domain.SourceLocale)
		if planErr != nil {
			return planErr
		}
		if locale != domain.SourceLocale {
			targetPath = true
			contributors, contributorErr := pageContributorUUIDs(req.Msg.ContributorMemberIds)
			if contributorErr != nil {
				return contributorErr
			}
			targetResult, targetRevision, err = applyPageTargetLocaleBatch(
				ctx, tx, s.contentBlocks, req.Msg.PageId, documentID, locale,
				contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors},
				req.Msg.ExpectedTargetRevision,
				pageTargetMetadataPatch{UpdateTitle: plan.UpdateTitle, Title: plan.Title, UpdateSummary: plan.UpdateSummary, Summary: plan.Summary},
				false, false, now, pageLockedTargetFence(documentID, domain),
			)
			if err != nil {
				return err
			}
			if targetResult.Changed {
				if err := tx.WithContext(ctx).Model(&model.Page{}).Where("id = ?", req.Msg.PageId).UpdateColumn("updated_at", now).Error; err != nil {
					return err
				}
			}
			if !targetResult.Changed {
				return nil
			}
			if plan.UpdateTitle {
				_, err = s.og.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, req.Msg.PageId, locale, false, "page_locale_metadata_saved")
				if err != nil {
					return err
				}
			}
			return appendPageMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PageId, locale,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		if req.Msg.ExpectedTargetRevision != nil {
			return errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
		}
		advance, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{
				DocumentID:       documentID,
				ExpectedRevision: expectedRevision,
			},
			pageSourceRoomFence(req.Msg.PageId, locale, pageLockedTargetFence(documentID, domain)),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				var mutationErr error
				mutation, mutationErr = applyPageLocaleMetadataMutation(
					ctx, tx, req.Msg.PageId, plan, now,
				)
				return mutation.Effect, mutationErr
			},
		)
		if err != nil {
			return err
		}
		if advance.Changed {
			if err := tx.WithContext(ctx).Model(&model.Page{}).
				Where("id = ?", req.Msg.PageId).
				UpdateColumn("updated_at", now).Error; err != nil {
				return err
			}
		}
		if !advance.Changed {
			return nil
		}
		if !mutation.TitleChanged {
			return appendPageMemberLocaleContentAudit(
				ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PageId, locale,
				sharedtelemetry.AuditItemOperationUpdated,
			)
		}
		_, err = s.og.RequestCurrentWithDB(
			ctx,
			tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			req.Msg.PageId,
			plan.Locale,
			false,
			"page_locale_metadata_saved",
		)
		if err != nil {
			return err
		}
		return appendPageMemberLocaleContentAudit(
			ctx, tx, s.auditWriter, contributorMemberID, req.Msg.PageId, locale,
			sharedtelemetry.AuditItemOperationUpdated,
		)
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	changed := advance.Changed
	documentRevision := advance.DocumentRevision.String()
	sourceChanged := advance.TranslationSourceChanged
	if targetPath {
		changed = targetResult.Changed
		documentRevision = targetResult.DocumentRevision.String()
		sourceChanged = false
	}
	if changed {
		fields := make([]string, 0, 2)
		if req.Msg.Title != nil {
			fields = append(fields, "title")
		}
		if req.Msg.SummaryChange != nil {
			fields = append(fields, "summary")
		}
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildPageContentUpdatedEvent(
			req.Msg.PageId,
			fields,
			documentRevision,
			req.Msg.ContributorMemberIds,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			locale,
			true,
			targetRevision,
			!targetPath,
		))
	}
	return connect.NewResponse(&intrav1.UpdatePageLocaleMetadataResponse{
		DocumentRevision: documentRevision, Changed: changed, SourceChanged: sourceChanged,
		ChangedLocales: []string{locale}, Locale: locale,
		TargetRevision: targetRevision,
	}), nil
}

func applyPageLocaleMetadataMutation(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	plan pageLocaleMetadataMutationPlan,
	now time.Time,
) (pageLocaleMetadataMutationResult, error) {
	var root struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table("page").Select("source_locale").Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		return pageLocaleMetadataMutationResult{}, err
	}
	if plan.Locale != root.SourceLocale {
		return pageLocaleMetadataMutationResult{}, errs.FailedPrecondition("target Page locale metadata is read-only")
	}
	var row struct {
		Title   *string `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	result := tx.WithContext(ctx).Raw(`
		SELECT title, summary
		FROM page_translation
		WHERE entity_id = ? AND locale = ?
		FOR UPDATE
	`, pageID, plan.Locale).Scan(&row)
	if result.Error != nil {
		return pageLocaleMetadataMutationResult{}, errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return pageLocaleMetadataMutationResult{}, errs.NotFound("page_translation", pageID+":"+plan.Locale)
	}

	nextTitle := cloneOptionalString(row.Title)
	nextSummary := cloneOptionalString(row.Summary)
	if plan.UpdateTitle {
		nextTitle = cloneOptionalString(plan.Title)
	}
	if plan.UpdateSummary {
		nextSummary = cloneOptionalString(plan.Summary)
	}
	titleChanged := plan.UpdateTitle && !nullableStringEqual(row.Title, nextTitle)
	summaryChanged := plan.UpdateSummary && !nullableStringEqual(row.Summary, nextSummary)
	changed := titleChanged || summaryChanged
	sourceChanged := changed && plan.Locale == root.SourceLocale
	output := pageLocaleMetadataMutationResult{
		Effect: contentblock.MetadataEffect{
			Changed:                  changed,
			AffectsTranslationSource: sourceChanged,
		},
		Title:          nextTitle,
		Summary:        nextSummary,
		TitleChanged:   titleChanged,
		SummaryChanged: summaryChanged,
	}
	if !changed {
		return output, nil
	}
	updates := structured.Fields{"updated_at": now}
	if titleChanged {
		updates["title"] = nextTitle
	}
	if summaryChanged {
		updates["summary"] = nextSummary
	}
	update := tx.WithContext(ctx).
		Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, plan.Locale).
		Updates(updates)
	if update.Error != nil {
		return pageLocaleMetadataMutationResult{}, errs.Internal(update.Error)
	}
	if update.RowsAffected != 1 {
		return pageLocaleMetadataMutationResult{}, errs.FailedPrecondition("Page locale metadata changed; reload before saving")
	}
	return output, nil
}

func (s *InternalPageService) UpdatePageDocumentMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdatePageDocumentMetadataRequest],
) (*connect.Response[intrav1.UpdatePageDocumentMetadataResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	if _, err := parsePageContentUUID("page_id", req.Msg.PageId); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	nextLayout, err := model.DocumentLayoutFromProto(req.Msg.DocumentLayout)
	if err != nil {
		return nil, errs.InvalidArgument("document_layout", err.Error())
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, req.Msg.PageId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var advance contentblock.AdvanceResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		advance, err = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{
				DocumentID:       documentID,
				ExpectedRevision: expectedRevision,
			},
			pageSourceRoomFence(req.Msg.PageId, locale, pageCollaborationDocumentFence(
				s.spiceDB, req.Msg.PageId, policyv1.Page.Manage, req.Msg.ContributorMemberIds,
			)),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				var row struct {
					DocumentLayout model.DocumentLayout `gorm:"column:document_layout"`
				}
				if err := tx.WithContext(ctx).
					Table("page").
					Select("document_layout").
					Where("id = ?", req.Msg.PageId).
					Take(&row).Error; err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if row.DocumentLayout == nextLayout {
					return contentblock.MetadataEffect{}, nil
				}
				if err := tx.WithContext(ctx).Model(&model.Page{}).
					Where("id = ?", req.Msg.PageId).
					Updates(structured.Fields{
						"document_layout": nextLayout,
						"updated_at":      now,
					}).Error; err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if s.auditWriter != nil {
					if err := domainaudit.AppendRequest(
						ctx,
						tx,
						s.auditWriter,
						sharedtelemetry.AuditPageUpdated,
						func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
							return sharedtelemetry.NewPageConfigurationAuditRecord(
								metadata, req.Msg.PageId, []string{"document_layout"},
							)
						},
					); err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				return contentblock.MetadataEffect{Changed: true}, nil
			},
		)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	if advance.Changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildContentUpdatedEvent(
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
			req.Msg.PageId,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			buildContentUpdatedFields([]string{"documentLayout"}, pageContentUpdatedFieldSpecs),
			false,
			advance.DocumentRevision.String(),
			req.Msg.ContributorMemberIds,
			nil,
		))
	}
	return connect.NewResponse(&intrav1.UpdatePageDocumentMetadataResponse{
		DocumentRevision: advance.DocumentRevision.String(), Changed: advance.Changed,
		SourceChanged: advance.TranslationSourceChanged, Locale: locale,
	}), nil
}

func (s *InternalPageService) CreatePageVersionCheckpoint(
	ctx context.Context,
	req *connect.Request[intrav1.CreatePageVersionCheckpointRequest],
) (*connect.Response[intrav1.CreatePageVersionCheckpointResponse], error) {
	if err := s.requireContentBlockStore(); err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	if _, err := parsePageContentUUID("page_id", req.Msg.PageId); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, req.Msg.PageId)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	var created *model.PageVersion
	var revision string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		domain, fenceErr := pageSourceRoomFence(req.Msg.PageId, locale, pageCollaborationDocumentFence(
			s.spiceDB, req.Msg.PageId, policyv1.Page.Edit, req.Msg.ContributorMemberIds,
		))(ctx, tx, documentID)
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
		revision = snapshot.Document.Revision.String()
		metadata, metadataErr := loadRequiredPageSourceLocaleMetadata(ctx, tx, req.Msg.PageId)
		if metadataErr != nil {
			return metadataErr
		}
		document, materializeErr := MaterializeLocalizedPageContentDocument(
			snapshot, domain.SourceLocale,
		)
		if materializeErr != nil {
			return materializeErr
		}
		encoded, encodeErr := EncodeVersionContentSnapshot(
			domain.SourceLocale, metadata.Title, metadata.Summary, document,
		)
		if encodeErr != nil {
			return encodeErr
		}

		var previous model.PageVersion
		previousResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("page_id = ?", req.Msg.PageId).
			Order("version DESC").
			First(&previous)
		if previousResult.Error == nil {
			previousSnapshot, decodeErr := DecodeVersionContentSnapshot(previous.ContentSnapshot)
			if decodeErr != nil {
				return decodeErr
			}
			canonicalPrevious, encodePreviousErr := EncodeVersionContentSnapshot(
				previousSnapshot.SourceLocale,
				previousSnapshot.Title,
				previousSnapshot.Summary,
				previousSnapshot.Document,
			)
			if encodePreviousErr != nil {
				return encodePreviousErr
			}
			if bytes.Equal(canonicalPrevious, encoded) {
				return nil
			}
		}
		if previousResult.Error != nil && !errors.Is(previousResult.Error, gorm.ErrRecordNotFound) {
			return previousResult.Error
		}
		nextVersion := int32(1)
		if previousResult.Error == nil {
			nextVersion = previous.Version + 1
		}
		created = &model.PageVersion{
			PageID:               req.Msg.PageId,
			Version:              nextVersion,
			Title:                cloneOptionalString(metadata.Title),
			Summary:              cloneOptionalString(metadata.Summary),
			ContentSnapshot:      encoded,
			ContributorMemberIDs: normalizeContributorMemberIDs(req.Msg.ContributorMemberIds),
			CreatedAt:            time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Create(created).Error; err != nil {
			return err
		}
		return s.appendPageVersionCheckpointAudit(ctx, tx, req.Msg.PageId, created)
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	response := &intrav1.CreatePageVersionCheckpointResponse{
		Created:  created != nil,
		Revision: revision,
		Locale:   locale,
	}
	if created != nil {
		response.VersionId = &created.ID
	}
	return connect.NewResponse(response), nil
}

func (s *InternalPageService) appendPageVersionCheckpointAudit(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	version *model.PageVersion,
) error {
	if version == nil || s.auditWriter == nil {
		return nil
	}
	return domainaudit.AppendVersion(
		ctx,
		tx,
		s.auditWriter,
		sharedtelemetry.AuditPageUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageVersionCreatedAuditRecord(
				metadata, pageID, version.ID, version.ContributorMemberIDs,
			)
		},
	)
}
