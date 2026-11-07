package page

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// FileDeliveryResolver projects exact File IDs after Page authorization.
type FileDeliveryResolver interface {
	ResolveAuthorizedPageFeaturedImage(context.Context, string, string) (*fileref.Delivery, error)
}

// Deps are the collaborators of the Page use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // pagepostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	// Files deliver the authorized featured image of a Page.
	Files FileDeliveryResolver
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Files": d.Files == nil,
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
	units     mutation.UnitOfWork[Store]
	spiceDB   Authorizer
	files     FileDeliveryResolver
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Page use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, spiceDB: deps.SpiceDB, files: deps.Files, mutations: mutations}, nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, Store) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: access,
		Change: func(ctx context.Context, store Store) (struct{}, error) { return struct{}{}, read(ctx, store) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// Paging selects a page of a list. A zero Limit reads the list's default.
type Paging struct {
	Limit  int32
	Offset int32
}

func (p Paging) limitOr(fallback int32) int32 {
	if p.Limit > 0 {
		return p.Limit
	}
	return fallback
}

// Pagination is where a page sits among every matching row.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

type GetPageQuery struct{ ID string }
type GetPageBySlugQuery struct{ Slug string }
type ListPagesAdminQuery struct {
	Page    Paging
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
	AccessPolicy   *pageaccess.Policy
	AcceptLanguage string
}
type DeletePageCommand struct{ ID string }
type PublishPageCommand struct{ ID string }
type UnpublishPageCommand struct{ ID string }

// GetPage reads a Page the caller may view.
func (s *Service) GetPage(ctx context.Context, query GetPageQuery) (_ Detail, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.ID))

	return s.pageBy(ctx, ByID, query.ID)
}

// GetPageBySlug reads a Page the caller may view by its slug.
func (s *Service) GetPageBySlug(ctx context.Context, query GetPageBySlugQuery) (Detail, error) {
	return s.pageBy(ctx, BySlug, query.Slug)
}

// PageList is one page of Pages.
type PageList struct {
	Pages      []View
	Pagination Pagination
}

// ListPagesAdmin returns a page of Pages to a caller who may list them.
func (s *Service) ListPagesAdmin(ctx context.Context, query ListPagesAdminQuery) (PageList, error) {
	if err := requirePageList(ctx, s.spiceDB); err != nil {
		return PageList{}, err
	}
	limit, offset := query.Page.limitOr(20), query.Page.Offset
	var pages []Page
	var total int64
	var sources map[string]SourceMetadata
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if pages, total, err = store.ListPages(ctx, ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: limit, Offset: offset}); err != nil {
			return err
		}
		sources, err = store.SourceMetadata(ctx, pageIDs(pages))
		return err
	}); err != nil {
		return PageList{}, err
	}
	views := make([]View, len(pages))
	for i, page := range pages {
		source, ok := sources[page.ID]
		if !ok {
			return PageList{}, domainerr.ResourceNotFound("page_translation", page.ID)
		}
		views[i] = newView(page, &source)
	}
	return PageList{Pages: views, Pagination: Pagination{
		Total: int32(total), Limit: limit, Offset: offset, HasMore: int64(offset)+int64(limit) < total,
	}}, nil
}

func pageIDs(pages []Page) []string {
	ids := make([]string, 0, len(pages))
	for _, page := range pages {
		ids = append(ids, page.ID)
	}
	return ids
}

// pageCreation is a validated Page creation.
type pageCreation struct {
	command      CreatePageCommand
	title        string
	accessPolicy pageaccess.Policy
	page         *Page
}

// CreatePage creates a draft Page and its empty content document.
func (s *Service) CreatePage(ctx context.Context, command CreatePageCommand) (Detail, error) {
	creation, err := s.preparePageCreation(ctx, command)
	if err != nil {
		return Detail{}, err
	}
	page, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[*Page, Store]{
		Access: freshPageCreate(),
		Change: func(ctx context.Context, store Store) (*Page, error) {
			return s.createPage(ctx, store, creation)
		},
		Audit: func(page *Page) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditPageCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageCreatedAuditRecord(metadata, page.ID)
			})
		},
		Relationships: func(page *Page) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
		if errors.Is(err, ErrSlugTaken) {
			return Detail{}, domainerr.DuplicateSlug("page", creation.page.Slug).WithCause(err)
		}
		return Detail{}, err
	}
	return s.pageBy(ctx, ByID, page.ID)
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
	page := &Page{
		DocumentLayout: DefaultDocumentLayout(),
		AccessPolicy:   encodedPolicy,
		Status:         StatusDraft,
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

func (s *Service) createPage(ctx context.Context, store Store, creation pageCreation) (*Page, error) {
	page := *creation.page
	if err := validatePageAccessPolicyTags(ctx, store, &creation.accessPolicy); err != nil {
		return nil, err
	}
	if page.Slug != nil {
		if err := lockAvailablePageSlug(ctx, store, *page.Slug, ""); err != nil {
			return nil, err
		}
	}
	sourceLocale, err := resolveCreateSourceLocale(ctx, store, creation.command.SourceLocale, creation.command.AcceptLanguage)
	if err != nil {
		return nil, err
	}
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      pageContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	page.ContentDocumentID = &contentDocumentID
	page.SourceLocale = sourceLocale
	if err := store.InsertPage(ctx, &page); err != nil {
		return nil, err
	}
	if err := createInitialPageSourceLocaleMetadata(
		ctx, store, page.ID, sourceLocale, creation.title, creation.command.Summary, time.Now().UTC(),
	); err != nil {
		return nil, err
	}
	if _, err := store.RequestOG(ctx, page.ID, sourceLocale, false, "page_created"); err != nil {
		return nil, err
	}
	return &page, nil
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

	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageDeletion, Store]{
		Access: lockedPage(command.ID, policyv1.Page.Delete),
		Change: func(ctx context.Context, store Store) (pageDeletion, error) {
			return s.deletePage(ctx, store, command.ID)
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

func (s *Service) deletePage(ctx context.Context, store Store, pageID string) (pageDeletion, error) {
	locked, err := lockPageSettings(ctx, store, pageID)
	if err != nil {
		return pageDeletion{}, err
	}
	deletion, err := s.pageDeletionRelationships(ctx, pageID)
	if err != nil {
		return pageDeletion{}, err
	}
	if err := store.DeleteShareLinks(ctx, pageID); err != nil {
		return pageDeletion{}, err
	}
	if err := store.CancelOG(ctx, pageID); err != nil {
		return pageDeletion{}, err
	}
	if err := store.ReleaseExactFeaturedImageBinding(ctx, pageID); err != nil {
		return pageDeletion{}, err
	}
	if err := store.RemoveMenuTargets(ctx, pageID, derefString(locked.Slug)); err != nil {
		return pageDeletion{}, err
	}
	if locked.ContentDocumentID == nil || *locked.ContentDocumentID == uuid.Nil {
		return pageDeletion{}, domainerr.Precondition("page content document has not been populated")
	}
	if err := store.DeleteContentDocument(ctx, *locked.ContentDocumentID, lockedPageContentFence(pageID)); err != nil {
		return pageDeletion{}, normalizePageContentBlockError(err)
	}
	// Deleting the Content Document cascades only Block-owned locale and File
	// reference rows. File and in-flight upload lifecycles remain File-owned.
	if err := store.DeletePage(ctx, pageID); err != nil {
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
	status   Status
	previous sharedtelemetry.AuditState
	next     sharedtelemetry.AuditState
	fields   []string
	// admit rejects a transition the Page content does not allow.
	admit func(ctx context.Context, store Store, page *Page) error
}

// LifecycleResult is the Page a lifecycle transition leaves and whether it
// changed it.
type LifecycleResult struct {
	Page    Page
	Changed bool
}

// PublishPage publishes a Page whose content media is ready.
func (s *Service) PublishPage(ctx context.Context, command PublishPageCommand) (_ LifecycleResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	return s.transitionPage(ctx, command.ID, pageLifecycle{
		action: policyv1.Page.Publish, status: StatusPublished,
		previous: sharedtelemetry.AuditStateDraft, next: sharedtelemetry.AuditStatePublished,
		fields: []string{"state.status", "state.published_at"},
		admit:  requirePublicationReady,
	})
}

// UnpublishPage moves a Page back to draft.
func (s *Service) UnpublishPage(ctx context.Context, command UnpublishPageCommand) (_ LifecycleResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.ID))

	return s.transitionPage(ctx, command.ID, pageLifecycle{
		action: policyv1.Page.Publish, status: StatusDraft,
		previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft,
		fields: []string{"state.status"},
	})
}

func (s *Service) transitionPage(ctx context.Context, pageID string, lifecycle pageLifecycle) (LifecycleResult, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[LifecycleResult, Store]{
		Access: lockedPage(pageID, lifecycle.action),
		Change: func(ctx context.Context, store Store) (LifecycleResult, error) {
			return applyPageLifecycle(ctx, store, pageID, lifecycle)
		},
		Audit: func(transition LifecycleResult) mutation.Audit {
			if !transition.Changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageLifecycleAuditRecord(metadata, pageID, lifecycle.previous, lifecycle.next)
			})
		},
		Outbox: func(ctx context.Context, store Store, transition LifecycleResult) error {
			if !transition.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: pageID, Kind: StateUpdate, Fields: lifecycle.fields})
		},
	})
}

func applyPageLifecycle(ctx context.Context, store Store, pageID string, lifecycle pageLifecycle) (LifecycleResult, error) {
	page, err := store.FindPage(ctx, ByID, pageID, UpdateLock)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return LifecycleResult{}, domainerr.ResourceNotFound("page", pageID)
		}
		return LifecycleResult{}, err
	}
	if page.Status == lifecycle.status {
		return LifecycleResult{Page: page}, nil
	}
	if lifecycle.admit != nil {
		if err := lifecycle.admit(ctx, store, &page); err != nil {
			return LifecycleResult{}, err
		}
	}
	now := dbtime.Now()
	publishes := lifecycle.status == StatusPublished
	var publishedAt *time.Time
	if publishes && page.PublishedAt == nil {
		publishedAt = &now
	}
	if err := store.SetStatus(ctx, pageID, lifecycle.status, publishedAt, now); err != nil {
		return LifecycleResult{}, err
	}
	page.Status = lifecycle.status
	page.UpdatedAt = now
	if publishedAt != nil {
		page.PublishedAt = publishedAt
	}
	return LifecycleResult{Page: page, Changed: true}, nil
}

// requirePublicationReady rejects publishing a Page whose content document
// or attached media is not ready.
func requirePublicationReady(ctx context.Context, store Store, page *Page) error {
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
	attachments, err := store.PublicationAttachments(ctx, documentID)
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	return requireAttachmentsReadyForPublication(ctx, store, attachments)
}

// lockPageSettings locks the Page row and reads its configuration; a
// missing Page answers not found.
func lockPageSettings(ctx context.Context, store Store, pageID string) (Settings, error) {
	settings, err := store.LockSettings(ctx, pageID)
	if errors.Is(err, ErrPageNotFound) {
		return Settings{}, domainerr.ResourceNotFound("page", pageID)
	}
	return settings, err
}
