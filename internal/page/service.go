package page

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// pageSortConfig defines allowed sort fields for pages
var pageSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":        PageSourceTitleSQL("page"),
		"slug":         "slug",
		"status":       "status",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"show_title":   "show_title",
	},
	DefaultSort: "created_at DESC",
}

// PageService implements the PageService Connect handler
type PageService struct {
	managev1connect.UnimplementedPageServiceHandler
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	runtime        Runtime
	fileService    FileDeleter
	asyncPublisher AsyncPublisher
	identityGetter auth.IdentityGetter
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	mediaHydrator  ContentBlockMediaHydrator
	menuTargets    MenuTargets
}

type PageServiceOption func(*PageService)

func WithPageContentBlockStore(store *contentblock.Store) PageServiceOption {
	return func(service *PageService) {
		service.contentBlocks = store
	}
}

func WithPageContentBlockMediaHydrator(hydrator ContentBlockMediaHydrator) PageServiceOption {
	return func(service *PageService) {
		service.mediaHydrator = hydrator
	}
}

func WithPageMenuTargets(targets MenuTargets) PageServiceOption {
	return func(service *PageService) {
		service.menuTargets = targets
	}
}

func NewAuditedPageService(
	db *gorm.DB,
	runtime Runtime,
	fileService FileDeleter,
	asyncPublisher AsyncPublisher,
	identityGetter auth.IdentityGetter,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	options ...PageServiceOption,
) *PageService {
	if auditWriter == nil {
		panic("page audit writer is required")
	}
	service := NewPageService(
		db, runtime, fileService, asyncPublisher, identityGetter, spiceDB, options...,
	)
	service.auditWriter = auditWriter
	return service
}

type lockedPageMenuTargetState struct {
	Slug              *string    `gorm:"column:slug"`
	ShowTitle         bool       `gorm:"column:show_title"`
	ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func lockPageMenuTargetStateForUpdate(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (*lockedPageMenuTargetState, error) {
	var state lockedPageMenuTargetState
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table("page").
		Select("slug", "show_title", "content_document_id", "updated_at").
		Where("id = ?", pageID).
		Take(&state).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("page", pageID)
		}
		return nil, errs.Internal(err)
	}
	return &state, nil
}

// NewPageService creates a new PageService
func NewPageService(
	db *gorm.DB,
	runtime Runtime,
	fileService FileDeleter,
	asyncPublisher AsyncPublisher,
	identityGetter auth.IdentityGetter,
	spiceDB *auth.SpiceDBClient,
	options ...PageServiceOption,
) *PageService {
	dependencycheck.New("PageService").
		RequireNotNil(db, "db").
		RequireNotNil(runtime, "runtime").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(fileService, "fileService").
		RequireNotNil(asyncPublisher, "asyncPublisher").
		Validate()
	service := &PageService{
		db:             db,
		spiceDB:        spiceDB,
		runtime:        runtime,
		fileService:    fileService,
		asyncPublisher: asyncPublisher,
		identityGetter: identityGetter,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// GetPage retrieves a page by ID.
// Published pages are accessible to any authenticated user; draft pages require admin.
func (s *PageService) GetPage(
	ctx context.Context,
	req *connect.Request[managev1.GetPageRequest],
) (*connect.Response[managev1.Page], error) {
	return s.getPageBy(ctx, "id", req.Msg.Id)
}

func (s *PageService) getPageBy(
	ctx context.Context,
	column string,
	value string,
) (*connect.Response[managev1.Page], error) {
	return s.pageResponseByWithReadyOg(ctx, column, value)
}

// GetPageBySlug retrieves a page by slug.
// Published pages are accessible to any authenticated user; draft pages require admin.
func (s *PageService) GetPageBySlug(
	ctx context.Context,
	req *connect.Request[managev1.GetPageBySlugRequest],
) (*connect.Response[managev1.Page], error) {
	return s.getPageBy(ctx, "slug", req.Msg.Slug)
}

// ListPagesAdmin returns a paginated list of pages (admin only)
func (s *PageService) ListPagesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListPagesAdminRequest],
) (*connect.Response[managev1.ListPagesAdminResponse], error) {
	// Check admin permission
	if err := requirePageList(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	var pages []model.Page
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Page{})

	// Apply filters using FilterConfig
	query, err := PageFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting with whitelist validation
	query, err = pageSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&pages).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayPageSourceLocaleMetadataForPages(ctx, pages); err != nil {
		return nil, err
	}

	// Convert to proto (summary without content)
	protoPages := make([]*managev1.PageSummary, len(pages))
	for i := range pages {
		protoPages[i] = s.toProtoPageSummary(&pages[i])
	}

	return connect.NewResponse(&managev1.ListPagesAdminResponse{
		Pages: protoPages,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreatePage creates a new page (admin only)
func (s *PageService) CreatePage(
	ctx context.Context,
	req *connect.Request[managev1.CreatePageRequest],
) (*connect.Response[managev1.Page], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}

	// Validate required fields
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, errs.Required("title")
	}
	normalizedSlug, slugPresent := normalizeOptionalNullableString(req.Msg.Slug)

	// Check slug uniqueness before creating
	if slugPresent && normalizedSlug != nil {
		if err := s.checkSlugAvailable(ctx, *normalizedSlug, ""); err != nil {
			return nil, err
		}
	}

	page := &model.Page{
		DocumentLayout: model.DefaultDocumentLayout(),
		Status:         model.PageStatus(managev1.PageStatus_PAGE_STATUS_DRAFT.String()),
		ShowTitle:      true, // Default
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if slugPresent {
		page.Slug = normalizedSlug
	}
	if req.Msg.ShowTitle != nil {
		page.ShowTitle = *req.Msg.ShowTitle
	}
	// Note: featured_image_file_id is set via SetPageFeaturedImage RPC

	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if err := requireLockedPageCreate(ctx, tx, s.spiceDB); err != nil {
			return err
		}
		if page.Slug != nil {
			if err := routeregistry.LockPageRouteConflict(ctx, tx, *page.Slug); err != nil {
				return err
			}
			if err := ensureSlugAvailable(ctx, tx, &model.Page{}, "page", *page.Slug, ""); err != nil {
				return err
			}
			if occupied, err := routeregistry.IsPageRouteOccupiedByResource(ctx, tx, *page.Slug); err != nil {
				return err
			} else if occupied {
				return errs.SlugAlreadyExists("page", *page.Slug)
			}
		}
		now := time.Now().UTC()
		sourceLocale := resolveInitialSourceLocale(ctx, tx, nil, req.Header().Get("Accept-Language"))
		document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile:      pageContentDocumentProfile,
			SourceLocale: sourceLocale,
		})
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		contentDocumentID := document.Document.ID.String()
		page.ContentDocumentID = &contentDocumentID
		page.SourceLocale = sourceLocale
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(page).Error; err != nil {
			return err
		}
		if err := createInitialPageSourceLocaleMetadata(
			ctx,
			tx,
			page.ID,
			sourceLocale,
			title,
			req.Msg.Summary,
			now,
		); err != nil {
			return err
		}
		_, err = s.runtime.RequestCurrentWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			page.ID, sourceLocale, false, "page_created",
		)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Page.TouchPolicy(page.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Page.DeletePolicy(page.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{policyTouch},
			[]policyv1.RelationshipMutation{policyDelete},
		); err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditPageCreated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewPageCreatedAuditRecord(metadata, page.ID)
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, errs.SlugAlreadyExists("page", "slug")
		}
		return nil, errs.Internal(err)
	}

	return s.pageResponseWithReadyOg(ctx, page)
}

// UpdatePage updates an existing page (admin only)
func (s *PageService) UpdatePage(
	ctx context.Context,
	req *connect.Request[managev1.UpdatePageRequest],
) (*connect.Response[managev1.UpdatePageResponse], error) {
	page := model.Page{ID: req.Msg.Id}
	update, err := s.preparePageUpdate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	changed, err := s.applyPageUpdate(ctx, &page, update)
	if err != nil {
		return nil, normalizePageUpdateError(err)
	}
	if changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManagePageContentUpdatedEvent(req.Msg))
	}
	return connect.NewResponse(&managev1.UpdatePageResponse{
		Id:        page.ID,
		Changed:   changed,
		Slug:      page.Slug,
		ShowTitle: page.ShowTitle,
		UpdatedAt: timestamppb.New(page.UpdatedAt),
	}), nil
}

// DeletePage deletes a page (admin only)
func (s *PageService) DeletePage(
	ctx context.Context,
	req *connect.Request[managev1.DeletePageRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}

	// First get the page so its domain relations can be removed atomically.
	var page model.Page
	if err := s.db.WithContext(ctx).First(&page, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("page", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		lockedPage, err := lockPageMenuTargetStateForUpdate(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Delete); err != nil {
			return err
		}
		snapshotPlan, err := policyv1.Page.Snapshot(page.ID)
		if err != nil {
			return err
		}
		snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		deleteRelationships, restoreRelationships, err := pageAuthorizationDeletionBatches(page.ID, snapshots)
		if err != nil {
			return err
		}
		if len(deleteRelationships) == 0 {
			return errs.InternalMsg("page authorization relationships are missing")
		}
		if err := tx.
			Where("entity_id = ? AND entity_type IN ?", page.ID, []string{
				"page",
				managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE.String(),
			}).
			Delete(&model.ShareLink{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.runtime.CancelAndReleaseEntityWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
			"page", page.ID,
		); err != nil {
			return err
		}
		if err := s.runtime.ReleaseExactPublicAssetBindings(
			ctx, tx, "page", page.ID, []string{"featured_image"},
		); err != nil {
			return err
		}
		if s.menuTargets == nil {
			return errs.InternalMsg("Page Menu target remover is not configured")
		}
		if err := s.menuTargets.Remove(ctx, tx, "page", page.ID, derefString(lockedPage.Slug)); err != nil {
			return err
		}
		if lockedPage.ContentDocumentID == nil || *lockedPage.ContentDocumentID == uuid.Nil {
			return errs.FailedPrecondition("page content document has not been populated")
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			*lockedPage.ContentDocumentID,
			lockedPageContentFence(page.ID),
		); err != nil {
			return normalizePageContentBlockError(err)
		}
		// Deleting the Content Document cascades only Block-owned locale and File
		// reference rows. File and in-flight upload lifecycles remain File-owned.
		if err := tx.Delete(&page).Error; err != nil {
			return err
		}
		if err := write(
			deleteRelationships,
			restoreRelationships,
		); err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditPageDeleted,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewPageDeletedAuditRecord(metadata, page.ID)
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// PublishPage publishes a page (admin only)
func (s *PageService) PublishPage(
	ctx context.Context,
	req *connect.Request[managev1.PublishPageRequest],
) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}
	var page model.Page
	lifecycleChanged := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&page, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("page", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Publish); err != nil {
			return err
		}
		if page.Status == model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()) {
			return nil
		}
		if page.ContentDocumentID == nil {
			return errs.FailedPrecondition("page content document has not been populated")
		}
		documentID, err := uuid.Parse(*page.ContentDocumentID)
		if err != nil || documentID == uuid.Nil {
			return errs.FailedPrecondition("page content document has not been populated")
		}
		attachments, err := s.contentBlocks.LoadPublicationAttachments(ctx, tx, documentID)
		if err != nil {
			return normalizePageContentBlockError(err)
		}
		if err := RequireIndexedContentBlockAttachmentsReadyForPublication(ctx, tx, attachments); err != nil {
			return err
		}
		now := time.Now()
		updates := structured.Fields{
			"status":     managev1.PageStatus_PAGE_STATUS_PUBLISHED.String(),
			"updated_at": now,
		}
		if page.PublishedAt == nil {
			updates["published_at"] = now
		}
		if err := tx.Model(&page).Updates(updates).Error; err != nil {
			return err
		}
		page.Status = model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String())
		page.UpdatedAt = now
		if page.PublishedAt == nil {
			page.PublishedAt = &now
		}
		lifecycleChanged = true
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageLifecycleAuditRecord(metadata, page.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStatePublished)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if lifecycleChanged {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageStateTransitionContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
				page.ID,
				[]string{"state.status", "state.published_at"},
			),
		)
	}

	response := &managev1.PageLifecycleMutationResponse{
		Id:        page.ID,
		Changed:   lifecycleChanged,
		Status:    managev1.PageStatus(managev1.PageStatus_value[string(page.Status)]),
		UpdatedAt: timestamppb.New(page.UpdatedAt),
	}
	if page.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*page.PublishedAt)
	}
	return connect.NewResponse(response), nil
}

// UnpublishPage unpublishes a page (admin only)
func (s *PageService) UnpublishPage(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishPageRequest],
) (*connect.Response[managev1.PageLifecycleMutationResponse], error) {
	var page model.Page
	lifecycleChanged := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&page, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("page", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := requireLockedPagePermission(ctx, tx, s.spiceDB, page.ID, policyv1.Page.Publish); err != nil {
			return err
		}
		if page.Status == model.PageStatus(managev1.PageStatus_PAGE_STATUS_DRAFT.String()) {
			return nil
		}
		now := time.Now()
		updates := structured.Fields{"status": managev1.PageStatus_PAGE_STATUS_DRAFT.String(), "updated_at": now}
		if err := tx.Model(&page).Updates(updates).Error; err != nil {
			return err
		}
		page.Status = model.PageStatus(managev1.PageStatus_PAGE_STATUS_DRAFT.String())
		page.UpdatedAt = now
		lifecycleChanged = true
		if s.auditWriter == nil {
			return nil
		}
		return domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPageLifecycleAuditRecord(metadata, page.ID, sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if lifecycleChanged {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageStateTransitionContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
				page.ID,
				[]string{"state.status"},
			),
		)
	}

	response := &managev1.PageLifecycleMutationResponse{
		Id:        page.ID,
		Changed:   lifecycleChanged,
		Status:    managev1.PageStatus(managev1.PageStatus_value[string(page.Status)]),
		UpdatedAt: timestamppb.New(page.UpdatedAt),
	}
	if page.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*page.PublishedAt)
	}
	return connect.NewResponse(response), nil
}

// SetPageFeaturedImage sets the featured image for a page (admin only)
