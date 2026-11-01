package page

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

// Deps are the collaborators of the Page use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Runtime requests OG images and releases Page media bindings.
	Runtime Runtime
	// Files deliver the authorized featured image of a Page.
	Files         FileDeliveryResolver
	Signals       contentsignal.Publisher
	ContentBlocks *contentblock.Store
	MediaHydrator ContentBlockMediaHydrator
	// MenuTargets keeps Menu links to a Page in step with its slug.
	MenuTargets MenuTargets
	Members     Members
	ShareLinks  ShareLinks
	Audit       domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Runtime": d.Runtime == nil, "Files": d.Files == nil,
		"Signals": d.Signals == nil, "ContentBlocks": d.ContentBlocks == nil, "MediaHydrator": d.MediaHydrator == nil,
		"MenuTargets": d.MenuTargets == nil, "Members": d.Members == nil, "ShareLinks": d.ShareLinks == nil,
		"Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("page dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Page use cases: management, the editor-collab document
// boundary and the AI document boundary. Adapters translate their requests
// into its commands and queries.
type Service struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
	// permissions decides Page actions; it is spiceDB.
	permissions   CollaborationPermissionChecker
	runtime       Runtime
	files         FileDeliveryResolver
	signals       contentsignal.Publisher
	contentBlocks *contentblock.Store
	mediaHydrator ContentBlockMediaHydrator
	menuTargets   MenuTargets
	members       Members
	shareLinks    ShareLinks
	mutations     *mutation.Runner
}

// NewService validates deps and returns the Page use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, permissions: deps.SpiceDB, runtime: deps.Runtime, files: deps.Files, signals: deps.Signals,
		contentBlocks: deps.ContentBlocks, mediaHydrator: deps.MediaHydrator, menuTargets: deps.MenuTargets,
		members: deps.Members, shareLinks: deps.ShareLinks, mutations: mutations,
	}, nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, *gorm.DB) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(ctx, tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// publish schedules content.updated in the mutation transaction so the
// signal commits or rolls back with the data.
func (s *Service) publish(ctx context.Context, tx *gorm.DB, event *managev1.ContentUpdatedEvent) error {
	return contentsignal.PublishInTransaction(ctx, tx, s.signals, event)
}

// Page selects a page of a list. A zero Limit reads the list's default.
type Page struct {
	Limit  int32
	Offset int32
}

func (p Page) limitOr(fallback int32) int32 {
	if p.Limit > 0 {
		return p.Limit
	}
	return fallback
}

type GetPageQuery struct{ ID string }
type GetPageBySlugQuery struct{ Slug string }
type ListPagesAdminQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CreatePageCommand creates a draft Page. AcceptLanguage picks its source
// locale when SourceLocale is empty and the creator has no preference.
type CreatePageCommand struct {
	Title          string
	Slug           *string
	Summary        *string
	ShowTitle      *bool
	SourceLocale   string
	AccessPolicy   *commonv1.PageAccessPolicy
	AcceptLanguage string
}
type DeletePageCommand struct{ ID string }
type PublishPageCommand struct{ ID string }
type UnpublishPageCommand struct{ ID string }

// GetPage reads a Page the caller may view.
func (s *Service) GetPage(ctx context.Context, query GetPageQuery) (_ *managev1.Page, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.ID))

	return s.pageBy(ctx, "id", query.ID)
}

// GetPageBySlug reads a Page the caller may view by its slug.
func (s *Service) GetPageBySlug(ctx context.Context, query GetPageBySlugQuery) (*managev1.Page, error) {
	return s.pageBy(ctx, "slug", query.Slug)
}

// ListPagesAdmin returns a page of Pages to a caller who may list them.
func (s *Service) ListPagesAdmin(ctx context.Context, query ListPagesAdminQuery) (*managev1.ListPagesAdminResponse, error) {
	if err := requirePageList(ctx, s.permissions); err != nil {
		return nil, err
	}

	var pages []model.Page
	var total int64
	stored, err := PageFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.Page{}), query.Filters)
	if err != nil {
		return nil, err
	}
	if err := stored.Count(&total).Error; err != nil {
		return nil, err
	}
	limit, offset := query.Page.limitOr(20), query.Page.Offset
	stored, err = pageSortConfig.ApplySort(stored, query.Sorts)
	if err != nil {
		return nil, err
	}
	if err := stored.Order("page.id ASC").Limit(int(limit)).Offset(int(offset)).Find(&pages).Error; err != nil {
		return nil, err
	}
	if err := s.overlayPageSourceLocaleMetadataForPages(ctx, pages); err != nil {
		return nil, err
	}

	summaries := make([]*managev1.PageSummary, len(pages))
	for i := range pages {
		if summaries[i], err = toProtoPageSummary(&pages[i]); err != nil {
			return nil, err
		}
	}
	return &managev1.ListPagesAdminResponse{
		Pages: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}, nil
}

// pageCreation is a validated Page creation.
type pageCreation struct {
	command      CreatePageCommand
	title        string
	accessPolicy *commonv1.PageAccessPolicy
	page         *model.Page
}

// CreatePage creates a draft Page and its empty content document.
func (s *Service) CreatePage(ctx context.Context, command CreatePageCommand) (*managev1.Page, error) {
	creation, err := s.preparePageCreation(ctx, command)
	if err != nil {
		return nil, err
	}
	page, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.Page]{
		Access: s.freshPageCreate(),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.Page, error) {
			return s.createPage(ctx, tx, creation)
		},
		Audit: func(page *model.Page) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditPageCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageCreatedAuditRecord(metadata, page.ID)
			})
		},
		Relationships: func(page *model.Page) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touch, err := policyv1.Page.TouchPolicy(page.ID)
			if err != nil {
				return nil, nil, err
			}
			remove, err := policyv1.Page.DeletePolicy(page.ID)
			if err != nil {
				return nil, nil, err
			}
			return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
		},
	})
	if err != nil {
		if !mutation.IsClassified(err) && dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("page", "slug", "slug").WithCause(err)
		}
		return nil, err
	}
	return s.pageBy(ctx, "id", page.ID)
}

// preparePageCreation validates command before the mutation and checks the
// requested slug against the current routes.
func (s *Service) preparePageCreation(ctx context.Context, command CreatePageCommand) (pageCreation, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return pageCreation{}, domainerr.Required("title")
	}
	slug, slugPresent := normalizeOptionalNullableString(command.Slug)
	accessPolicy, err := pageaccess.Normalize(command.AccessPolicy)
	if err != nil {
		return pageCreation{}, domainerr.InvalidField("access_policy", err.Error())
	}
	encodedPolicy, err := pageaccess.Encode(accessPolicy)
	if err != nil {
		return pageCreation{}, err
	}
	if slugPresent && slug != nil {
		if err := s.checkSlugAvailable(ctx, *slug, ""); err != nil {
			return pageCreation{}, err
		}
	}
	createdAt := dbtime.Now()
	page := &model.Page{
		DocumentLayout: model.DefaultDocumentLayout(),
		AccessPolicy:   encodedPolicy,
		Status:         model.PageStatusDraft,
		ShowTitle:      true,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	if slugPresent {
		page.Slug = slug
	}
	if command.ShowTitle != nil {
		page.ShowTitle = *command.ShowTitle
	}
	return pageCreation{command: command, title: title, accessPolicy: accessPolicy, page: page}, nil
}

func (s *Service) createPage(ctx context.Context, tx *gorm.DB, creation pageCreation) (*model.Page, error) {
	page := creation.page
	if err := s.validatePageAccessPolicyTags(ctx, tx, creation.accessPolicy); err != nil {
		return nil, err
	}
	if page.Slug != nil {
		if err := lockAvailablePageSlug(ctx, tx, *page.Slug, ""); err != nil {
			return nil, err
		}
	}
	sourceLocale, err := s.resolveCreateSourceLocale(ctx, tx, creation.command.SourceLocale, creation.command.AcceptLanguage)
	if err != nil {
		return nil, err
	}
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      pageContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	page.ContentDocumentID = &contentDocumentID
	page.SourceLocale = sourceLocale
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(page).Error; err != nil {
		return nil, err
	}
	if err := createInitialPageSourceLocaleMetadata(
		ctx, tx, page.ID, sourceLocale, creation.title, creation.command.Summary, time.Now().UTC(),
	); err != nil {
		return nil, err
	}
	if _, err := s.runtime.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, page.ID, sourceLocale, false, "page_created",
	); err != nil {
		return nil, err
	}
	return page, nil
}

// pageDeletion is a locked Page about to be deleted and the policy
// relationships its deletion removes and would restore.
type pageDeletion struct {
	deleteRelationships  []policyv1.RelationshipMutation
	restoreRelationships []policyv1.RelationshipMutation
}

// DeletePage deletes a Page with its content document, share links, media
// bindings and Menu links.
func (s *Service) DeletePage(ctx context.Context, command DeletePageCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageDeletion]{
		Access: s.lockedPage(command.ID, policyv1.Page.Delete),
		Change: func(ctx context.Context, tx *gorm.DB) (pageDeletion, error) {
			return s.deletePage(ctx, tx, command.ID)
		},
		Audit: func(pageDeletion) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditPageDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageDeletedAuditRecord(metadata, command.ID)
			})
		},
		Relationships: func(deletion pageDeletion) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return deletion.deleteRelationships, deletion.restoreRelationships, nil
		},
	})
	return err
}

func (s *Service) deletePage(ctx context.Context, tx *gorm.DB, pageID string) (pageDeletion, error) {
	locked, err := lockPageMenuTargetStateForUpdate(ctx, tx, pageID)
	if err != nil {
		return pageDeletion{}, err
	}
	deletion, err := s.pageDeletionRelationships(ctx, pageID)
	if err != nil {
		return pageDeletion{}, err
	}
	if err := s.shareLinks.DeletePageShareLinks(ctx, tx, pageID); err != nil {
		return pageDeletion{}, err
	}
	if err := s.runtime.CancelAndReleaseEntityWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, "page", pageID); err != nil {
		return pageDeletion{}, err
	}
	if err := s.runtime.ReleaseExactPublicAssetBindings(ctx, tx, "page", pageID, []string{"featured_image"}); err != nil {
		return pageDeletion{}, err
	}
	if err := s.menuTargets.Remove(ctx, tx, "page", pageID, derefString(locked.Slug)); err != nil {
		return pageDeletion{}, err
	}
	if locked.ContentDocumentID == nil || *locked.ContentDocumentID == uuid.Nil {
		return pageDeletion{}, domainerr.Precondition("page content document has not been populated")
	}
	if err := s.contentBlocks.DeleteDocument(ctx, tx, *locked.ContentDocumentID, lockedPageContentFence(pageID)); err != nil {
		return pageDeletion{}, normalizePageContentBlockError(err)
	}
	// Deleting the Content Document cascades only Block-owned locale and File
	// reference rows. File and in-flight upload lifecycles remain File-owned.
	if err := tx.Delete(&model.Page{ID: pageID}).Error; err != nil {
		return pageDeletion{}, err
	}
	return deletion, nil
}

// pageDeletionRelationships reads the Page policy relationships its
// deletion removes and their exact restore.
func (s *Service) pageDeletionRelationships(ctx context.Context, pageID string) (pageDeletion, error) {
	snapshotPlan, err := policyv1.Page.Snapshot(pageID)
	if err != nil {
		return pageDeletion{}, err
	}
	snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	if err != nil {
		return pageDeletion{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	var deletion pageDeletion
	deletion.deleteRelationships, deletion.restoreRelationships, err = pageAuthorizationDeletionBatches(pageID, snapshots)
	if err != nil {
		return pageDeletion{}, err
	}
	if len(deletion.deleteRelationships) == 0 {
		return pageDeletion{}, errors.New("page authorization relationships are missing")
	}
	return deletion, nil
}

// pageLifecycle is one Page publication transition.
type pageLifecycle struct {
	action   pageAction
	status   model.PageStatus
	previous sharedtelemetry.AuditState
	next     sharedtelemetry.AuditState
	fields   []string
	// admit rejects a transition the Page content does not allow.
	admit func(ctx context.Context, tx *gorm.DB, page *model.Page) error
}

// pageTransition is the outcome of a lifecycle transition.
type pageTransition struct {
	page    model.Page
	changed bool
}

// PublishPage publishes a Page whose content media is ready.
func (s *Service) PublishPage(ctx context.Context, command PublishPageCommand) (_ *managev1.PageLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	return s.transitionPage(ctx, command.ID, pageLifecycle{
		action: policyv1.Page.Publish, status: model.PageStatusPublished,
		previous: sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStatePublished,
		fields: []string{"state.status", "state.published_at"},
		admit:  s.requirePublicationReady,
	})
}

// UnpublishPage moves a Page back to draft.
func (s *Service) UnpublishPage(ctx context.Context, command UnpublishPageCommand) (_ *managev1.PageLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	return s.transitionPage(ctx, command.ID, pageLifecycle{
		action: policyv1.Page.Publish, status: model.PageStatusDraft,
		previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft,
		fields: []string{"state.status"},
	})
}

func (s *Service) transitionPage(ctx context.Context, pageID string, lifecycle pageLifecycle) (*managev1.PageLifecycleMutationResponse, error) {
	transition, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageTransition]{
		Access: s.lockedPage(pageID, lifecycle.action),
		Change: func(ctx context.Context, tx *gorm.DB) (pageTransition, error) {
			return applyPageLifecycle(ctx, tx, pageID, lifecycle)
		},
		Audit: func(transition pageTransition) mutation.Audit {
			if !transition.changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageLifecycleAuditRecord(metadata, pageID, lifecycle.previous, lifecycle.next)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, transition pageTransition) error {
			if !transition.changed {
				return nil
			}
			return s.publish(ctx, tx, buildManageStateTransitionContentUpdatedEvent(pageID, lifecycle.fields))
		},
	})
	if err != nil {
		return nil, err
	}
	page := transition.page
	response := &managev1.PageLifecycleMutationResponse{
		Id:        page.ID,
		Changed:   transition.changed,
		Status:    pageStatusToProto(page.Status),
		UpdatedAt: timestamppb.New(page.UpdatedAt),
	}
	if page.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*page.PublishedAt)
	}
	return response, nil
}

func applyPageLifecycle(ctx context.Context, tx *gorm.DB, pageID string, lifecycle pageLifecycle) (pageTransition, error) {
	var page model.Page
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&page, "id = ?", pageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pageTransition{}, domainerr.ResourceNotFound("page", pageID)
		}
		return pageTransition{}, err
	}
	if page.Status == lifecycle.status {
		return pageTransition{page: page}, nil
	}
	if lifecycle.admit != nil {
		if err := lifecycle.admit(ctx, tx, &page); err != nil {
			return pageTransition{}, err
		}
	}
	now := dbtime.Now()
	updates := structured.Fields{"status": string(lifecycle.status), "updated_at": now}
	publishes := lifecycle.status == model.PageStatusPublished
	if publishes && page.PublishedAt == nil {
		updates["published_at"] = now
	}
	if err := tx.Model(&page).Updates(updates).Error; err != nil {
		return pageTransition{}, err
	}
	page.Status = lifecycle.status
	page.UpdatedAt = now
	if publishes && page.PublishedAt == nil {
		page.PublishedAt = &now
	}
	return pageTransition{page: page, changed: true}, nil
}

// requirePublicationReady rejects publishing a Page whose content document
// or attached media is not ready.
func (s *Service) requirePublicationReady(ctx context.Context, tx *gorm.DB, page *model.Page) error {
	if page.ContentDocumentID == nil {
		return domainerr.Precondition("page content document has not been populated")
	}
	documentID, err := uuid.Parse(*page.ContentDocumentID)
	if err != nil {
		return domainerr.Precondition("page content document has not been populated").WithCause(err)
	}
	if documentID == uuid.Nil {
		return domainerr.Precondition("page content document has not been populated")
	}
	attachments, err := s.contentBlocks.LoadPublicationAttachments(ctx, tx, documentID)
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	return RequireIndexedContentBlockAttachmentsReadyForPublication(ctx, tx, attachments)
}

type lockedPageMenuTargetState struct {
	Slug              *string         `gorm:"column:slug"`
	ShowTitle         bool            `gorm:"column:show_title"`
	ContentDocumentID *uuid.UUID      `gorm:"column:content_document_id;type:uuid"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
	AccessPolicy      json.RawMessage `gorm:"column:access_policy"`
}

func lockPageMenuTargetStateForUpdate(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (*lockedPageMenuTargetState, error) {
	var state lockedPageMenuTargetState
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("page").
		Select("slug", "show_title", "content_document_id", "updated_at", "access_policy").
		Where("id = ?", pageID).
		Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("page", pageID)
		}
		return nil, err
	}
	return &state, nil
}
