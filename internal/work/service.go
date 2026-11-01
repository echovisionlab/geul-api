package work

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

func validateWorkDatePoint(yearField, monthField string, year, month int32) error {
	if year < 1 || year > 9999 {
		return domainerr.InvalidField(yearField, "must be between 1 and 9999")
	}
	if month < 1 || month > 12 {
		return domainerr.InvalidField(monthField, "must be between 1 and 12")
	}
	return nil
}

func validateWorkRange(year, month int32, untilYear, untilMonth *int32, isPresent bool) error {
	if err := validateWorkDatePoint("year", "month", year, month); err != nil {
		return err
	}

	if isPresent {
		if untilYear != nil || untilMonth != nil {
			return domainerr.InvalidField("until", "must be empty when is_present is true")
		}
		return nil
	}

	if untilYear == nil {
		return domainerr.Required("until_year")
	}
	if untilMonth == nil {
		return domainerr.Required("until_month")
	}
	if err := validateWorkDatePoint("until_year", "until_month", *untilYear, *untilMonth); err != nil {
		return err
	}
	if *untilYear < year || (*untilYear == year && *untilMonth < month) {
		return domainerr.InvalidField("until", "must not be earlier than from")
	}

	return nil
}

func sanitizeWorkMetadata(metadata structured.Fields) structured.Fields {
	if len(metadata) == 0 {
		return metadata
	}

	sanitized := make(structured.Fields, len(metadata))
	for key, value := range metadata {
		switch key {
		case "year", "month", "untilYear", "untilMonth", "isPresent", "periodYear", "periodMonth":
			continue
		default:
			sanitized[key] = value
		}
	}
	return sanitized
}

// Authorizer is the SpiceDB port of the Work use cases: the fresh checks
// and relationship writes of the mutation pipeline. *auth.SpiceDBClient
// implements it.
type Authorizer = mutation.Authorizer

// MemberSummaryLoader supplies the Member summaries of credits.
type MemberSummaryLoader interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

// Deps are the collaborators of the Work use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // workpostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	Members MemberSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Members": d.Members == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("work dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Work use cases: Site Admin management, credits,
// versions, the collaboration boundary and AI documents. Adapters translate
// their requests into its commands and queries.
type Service struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   Authorizer
	fence     identitystate.Fence
	members   MemberSummaryLoader
	mutations *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Work use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, fence: deps.Fence, members: deps.Members, mutations: mutations,
	}, nil
}

// read runs read in one read snapshot of the Work store.
func (s *Service) read(ctx context.Context, read func(Store) error) error {
	return s.units.Read(ctx, read)
}

// Page selects a page of a list. A zero Limit reads the list's default.
type Page struct {
	Limit  int32
	Offset int32
}

// Pagination is where a page sits among every match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

func paginationOf(pg queryutil.Pagination, total int64) Pagination {
	return Pagination{Total: int32(total), Limit: pg.Limit, Offset: pg.Offset, HasMore: pg.HasMore(total)}
}

// GetWorkQuery reads a Work for the Site Admin management surface.
type GetWorkQuery struct{ ID string }

// ListWorksAdminQuery lists every Work with its credit and client counts.
// The filters has_slug and has_featured_image test presence.
type ListWorksAdminQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// CreateWorkCommand creates a draft Work. SourceLocale, when empty, is the
// creator's preferred locale or one AcceptLanguage names; a nil Metadata
// stores none.
type CreateWorkCommand struct {
	Title          string
	Slug           *string
	Type           string
	Summary        *string
	Metadata       structured.Fields
	Featured       *bool
	Year           int32
	Month          int32
	UntilYear      *int32
	UntilMonth     *int32
	IsPresent      *bool
	SourceLocale   string
	Document       *contentv1.RichTextDocument
	AcceptLanguage string
}

type DeleteWorkCommand struct{ ID string }
type PublishWorkCommand struct{ ID string }
type UnpublishWorkCommand struct{ ID string }
type SetWorkFeaturedImageCommand struct{ WorkID, FileID string }
type DeleteWorkFeaturedImageCommand struct{ WorkID string }

// AdminWork is a Work of the admin list with its credit and client counts.
type AdminWork struct {
	View        View
	CreditCount int32
	ClientCount int32
}

// AdminWorkPage is one page of the admin list.
type AdminWorkPage struct {
	Works      []AdminWork
	Pagination Pagination
}

// GetWork retrieves a Work for the Site Admin management surface.
func (s *Service) GetWork(ctx context.Context, query GetWorkQuery) (View, error) {
	stored, err := s.findWork(ctx, query.ID)
	if err != nil {
		return View{}, err
	}
	if err := s.requireWorkViewOrNotFound(ctx, stored); err != nil {
		return View{}, err
	}
	work, err := s.viewWithSourceDocument(ctx, stored)
	if err != nil {
		return View{}, err
	}
	if err := s.hydrateWorkContentProjection(ctx, &work); err != nil {
		return View{}, err
	}
	work.FeaturedImage = s.getWorkFeaturedImageAsset(ctx, work.ID)
	if work.OgAsset, err = s.readyManageOgAssetRef(ctx, work.OgAssetID); err != nil {
		return View{}, err
	}
	if work.Clients, err = s.getWorkClients(ctx, work.ID); err != nil {
		return View{}, err
	}
	return work, nil
}

// findWork reads the Work row; a missing one answers not found.
func (s *Service) findWork(ctx context.Context, workID string) (Work, error) {
	var work Work
	err := s.read(ctx, func(store Store) error {
		var err error
		work, err = store.FindWork(ctx, workID)
		return err
	})
	if errors.Is(err, ErrWorkNotFound) {
		return Work{}, domainerr.ResourceNotFound("work", workID)
	}
	return work, err
}

// ListWorksAdmin returns a page of every Work with its credit and client
// counts (Site Admin-only).
func (s *Service) ListWorksAdmin(ctx context.Context, query ListWorksAdminQuery) (AdminWorkPage, error) {
	if err := requireWorkList(ctx, s.spiceDB); err != nil {
		return AdminWorkPage{}, err
	}
	pg := queryutil.ExtractPaginationRaw(query.Page.Limit, query.Page.Offset, queryutil.DefaultPaginationConfig)
	var stored []Work
	var total int64
	if err := s.read(ctx, func(store Store) error {
		var err error
		stored, total, err = store.ListWorks(ctx, AdminListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: pg.Limit, Offset: pg.Offset})
		return err
	}); err != nil {
		return AdminWorkPage{}, err
	}
	workIDs := collectWorkIDs(stored)
	sourceDocuments, err := s.loadSourceDocuments(ctx, workIDs)
	if err != nil {
		return AdminWorkPage{}, err
	}
	views := make([]View, len(stored))
	for i := range stored {
		if source, ok := sourceDocuments[stored[i].ID]; ok {
			views[i] = viewOf(stored[i], &source)
		} else {
			views[i] = viewOf(stored[i], nil)
		}
	}
	for i := range views {
		if err := s.hydrateWorkContentProjection(ctx, &views[i]); err != nil {
			return AdminWorkPage{}, err
		}
	}
	readyOgAssets, err := s.loadReadyWorkOgAssets(ctx, views)
	if err != nil {
		return AdminWorkPage{}, err
	}
	creditCounts := s.countWorkRows(ctx, Store.CountCredits, workIDs, "count_work_credits",
		"Work credit count lookup failed; listing works without credit counts")
	clientCounts := s.countWorkRows(ctx, Store.CountClients, workIDs, "count_work_clients",
		"Work client count lookup failed; listing works without client counts")
	works := make([]AdminWork, len(views))
	for i := range views {
		views[i].FeaturedImage = s.getWorkFeaturedImageAsset(ctx, views[i].ID)
		views[i].OgAsset = manageOgAssetFromReadyMap(readyOgAssets, views[i].OgAssetID)
		works[i] = AdminWork{View: views[i], CreditCount: creditCounts[views[i].ID], ClientCount: clientCounts[views[i].ID]}
	}
	return AdminWorkPage{Works: works, Pagination: paginationOf(pg, total)}, nil
}

// countWorkRows counts rows per Work. A failed count is logged and listed
// as zero.
func (s *Service) countWorkRows(
	ctx context.Context,
	count func(Store, context.Context, []string) (map[string]int32, error),
	workIDs []string,
	operation, message string,
) map[string]int32 {
	counts := make(map[string]int32)
	if len(workIDs) == 0 {
		return counts
	}
	if err := s.read(ctx, func(store Store) error {
		var err error
		counts, err = count(store, ctx, workIDs)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, message, "domain", "work", "operation", operation, failurecause.Attr(err))
		return map[string]int32{}
	}
	return counts
}

// CreateWork creates a draft Work (Site Admin-only).
func (s *Service) CreateWork(ctx context.Context, command CreateWorkCommand) (View, error) {
	title := strings.TrimSpace(command.Title)
	work, err := s.newWorkFromCreateCommand(ctx, command, title)
	if err != nil {
		return View{}, err
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Work, Store]{
		Access: mutation.FreshAdminCan(policyv1.Work.Create()),
		Change: func(ctx context.Context, store Store) (Work, error) {
			err := s.createWork(ctx, store, &work, title, command)
			return work, err
		},
		Audit: func(work Work) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditWorkCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkCreatedAuditRecord(metadata, work.ID)
			})
		},
		Outbox: func(ctx context.Context, store Store, work Work) error {
			_, err := requestWorkOG(ctx, store, work.ID, "", "work_created")
			return err
		},
		Relationships: workPolicyRelationships(true),
	})
	if err != nil {
		if errors.Is(err, ErrSlugTaken) && !mutation.IsClassified(err) {
			return View{}, domainerr.Duplicate("work", "slug", "slug").WithCause(err)
		}
		return View{}, err
	}
	loaded, err := s.loadCreatedWork(ctx, created.ID)
	if err != nil {
		return View{}, err
	}
	return s.workResponseWithReadyOg(ctx, loaded, nil)
}

// workPolicyRelationships returns the Work's policy relationship batch:
// touched when the Work is created, deleted when it is deleted, each with
// its exact inverse.
func workPolicyRelationships(created bool) func(Work) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	return func(work Work) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
		touch, err := policyv1.Work.TouchPolicy(work.ID)
		if err != nil {
			return nil, nil, err
		}
		remove, err := policyv1.Work.DeletePolicy(work.ID)
		if err != nil {
			return nil, nil, err
		}
		if created {
			return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
		}
		return []policyv1.RelationshipMutation{remove}, []policyv1.RelationshipMutation{touch}, nil
	}
}

func (s *Service) newWorkFromCreateCommand(
	ctx context.Context,
	command CreateWorkCommand,
	title string,
) (Work, error) {
	if title == "" {
		return Work{}, domainerr.Required("title")
	}
	if command.Type == "" {
		return Work{}, domainerr.Required("type")
	}
	if command.Year == 0 {
		return Work{}, domainerr.Required("year")
	}
	if command.Month == 0 {
		return Work{}, domainerr.Required("month")
	}
	isPresent := command.IsPresent != nil && *command.IsPresent
	untilYear, untilMonth := command.UntilYear, command.UntilMonth
	if isPresent {
		untilYear, untilMonth = nil, nil
	}
	if err := validateWorkRange(command.Year, command.Month, untilYear, untilMonth, isPresent); err != nil {
		return Work{}, err
	}
	slug, present := normalizeOptionalNullableString(command.Slug)
	if err := s.validateWorkSlug(ctx, slug); err != nil {
		return Work{}, err
	}
	work := Work{
		Type: command.Type, Year: command.Year, Month: command.Month,
		UntilYear: untilYear, UntilMonth: untilMonth, IsPresent: isPresent,
		Status: StatusDraft,
	}
	if present {
		work.Slug = slug
	}
	if command.Metadata != nil {
		work.Metadata = sanitizeWorkMetadata(command.Metadata)
	}
	if command.Featured != nil {
		work.Featured = *command.Featured
	}
	return work, nil
}

// validateWorkSlug validates a requested slug and requires, before the
// mutation, that no route uses it.
func (s *Service) validateWorkSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := pathslug.Validate(*slug); err != nil {
		return err
	}
	return s.read(ctx, func(store Store) error { return store.CheckResourceRoute(ctx, *slug) })
}

func (s *Service) createWork(
	ctx context.Context,
	store Store,
	work *Work,
	title string,
	command CreateWorkCommand,
) error {
	if work.Slug != nil && *work.Slug != "" {
		if err := store.LockResourceRoute(ctx, *work.Slug); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	sourceLocale, err := resolveCreateSourceLocale(ctx, store, command.SourceLocale, command.AcceptLanguage)
	if err != nil {
		return err
	}
	work.SourceLocale = sourceLocale
	if document := command.Document; document != nil {
		if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK {
			return domainerr.InvalidField("document.profile", "must be Work")
		}
		if document.GetSourceLocale() != sourceLocale {
			return domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
		}
	}
	createdDocument, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      workContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	documentID := createdDocument.Document.ID.String()
	work.ContentDocumentID = &documentID
	if err := store.InsertWork(ctx, work); err != nil {
		return err
	}
	if command.Document != nil {
		replace, err := contentblock.ReplaceFromRichTextProto(createdDocument.Document.ID, createdDocument.Document.Revision, command.Document)
		if err != nil {
			return normalizeWorkContentBlockError(err)
		}
		if _, err := store.ReplaceContentSnapshot(ctx, replace, workContentCreationFence(work.ID, sourceLocale)); err != nil {
			return normalizeWorkContentBlockError(err)
		}
	}
	return createInitialWorkSourceLocaleMetadata(ctx, store, work.ID, sourceLocale, title, command.Summary, now)
}

func (s *Service) loadCreatedWork(ctx context.Context, workID string) (View, error) {
	var stored Work
	if err := s.read(ctx, func(store Store) error {
		var err error
		stored, err = store.FindWork(ctx, workID)
		return err
	}); err != nil {
		return View{}, err
	}
	return s.viewWithSourceDocument(ctx, stored)
}

// DeleteWork deletes a Work after it has left the Archived lifecycle state
// (Site Admin-only).
func (s *Service) DeleteWork(ctx context.Context, command DeleteWorkCommand) error {
	work, err := s.findWork(ctx, command.ID)
	if err != nil {
		return err
	}
	var documentID uuid.UUID
	if err := s.read(ctx, func(store Store) error {
		var err error
		documentID, err = loadWorkContentDocumentID(ctx, store, work.ID, Unlocked)
		return err
	}); err != nil {
		return err
	}
	var locked Root
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[Work, Store]{
		Access: lockedWork(work.ID, policyv1.Work.Delete, &locked),
		Change: func(ctx context.Context, store Store) (Work, error) {
			if locked.Status == StatusArchived {
				return work, domainerr.Precondition("archived works must be published before deletion")
			}
			return work, deleteWork(ctx, store, work, documentID)
		},
		Audit: func(work Work) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditWorkDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkDeletedAuditRecord(metadata, work.ID)
			})
		},
		Relationships: workPolicyRelationships(false),
	})
	return err
}

func deleteWork(ctx context.Context, store Store, work Work, documentID uuid.UUID) error {
	if err := store.DeleteContentDocument(ctx, documentID, lockedWorkContentFence()); err != nil {
		return err
	}
	if err := store.DeleteShareLinks(ctx, work.ID); err != nil {
		return err
	}
	if err := store.CancelOG(ctx, work.ID); err != nil {
		return err
	}
	if err := store.ReleaseFeaturedImage(ctx, work.ID); err != nil {
		return err
	}
	return store.DeleteWork(ctx, work.ID)
}

// LifecycleChange is the outcome of a publish or unpublish.
type LifecycleChange struct {
	ID          string
	Changed     bool
	Status      Status
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// workLifecycleChange is a lifecycle change with what its audit and signal
// record.
type workLifecycleChange struct {
	LifecycleChange
	previous  sharedtelemetry.AuditState
	next      sharedtelemetry.AuditState
	published bool
}

func (change workLifecycleChange) audit() mutation.Audit {
	if !change.Changed {
		return mutation.Unchanged()
	}
	return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewWorkLifecycleAuditRecord(metadata, change.ID, change.previous, change.next)
	})
}

func publishLifecycleSignal(ctx context.Context, store Store, change workLifecycleChange) error {
	if !change.Changed {
		return nil
	}
	fields := []string{"state.status"}
	if change.published {
		fields = append(fields, "state.published_at")
	}
	return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: change.ID, Kind: StateUpdate, Fields: fields})
}

func lifecycleOf(work Work) LifecycleChange {
	return LifecycleChange{ID: work.ID, Status: work.Status, PublishedAt: work.PublishedAt, UpdatedAt: work.UpdatedAt}
}

// PublishWork publishes a draft Work or restores an archived one (Site
// Admin-only).
func (s *Service) PublishWork(ctx context.Context, command PublishWorkCommand) (LifecycleChange, error) {
	if err := s.requireStoredWork(ctx, command.ID); err != nil {
		return LifecycleChange{}, err
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[workLifecycleChange, Store]{
		Access: lockedWork(command.ID, policyv1.Work.Publish, nil),
		Change: func(ctx context.Context, store Store) (workLifecycleChange, error) {
			change := workLifecycleChange{next: sharedtelemetry.AuditStatePublished}
			work, err := store.FindWork(ctx, command.ID)
			if err != nil {
				return change, err
			}
			change.LifecycleChange = lifecycleOf(work)
			if work.Status == StatusPublished {
				return change, nil
			}
			now := dbtime.Now()
			var publishedAt *time.Time
			change.previous = sharedtelemetry.AuditStateDraft
			if work.Status == StatusArchived {
				change.previous = sharedtelemetry.AuditStateArchived
			} else {
				publishedAt = &now
				change.published = true
			}
			if err := store.SetStatus(ctx, command.ID, StatusPublished, publishedAt, now); err != nil {
				return change, err
			}
			change.Status = StatusPublished
			change.UpdatedAt = now
			if change.published {
				change.PublishedAt = &now
			}
			change.Changed = true
			return change, nil
		},
		Audit:  workLifecycleChange.audit,
		Outbox: publishLifecycleSignal,
	})
	if err != nil {
		return LifecycleChange{}, err
	}
	return change.LifecycleChange, nil
}

// UnpublishWork moves a published Work back to draft (Site Admin-only).
func (s *Service) UnpublishWork(ctx context.Context, command UnpublishWorkCommand) (LifecycleChange, error) {
	if err := s.requireStoredWork(ctx, command.ID); err != nil {
		return LifecycleChange{}, err
	}
	var locked Root
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[workLifecycleChange, Store]{
		Access: lockedWork(command.ID, policyv1.Work.Publish, &locked),
		Change: func(ctx context.Context, store Store) (workLifecycleChange, error) {
			change := workLifecycleChange{previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft}
			if locked.Status == StatusArchived {
				return change, domainerr.Precondition("archived works must be published before they can be unpublished")
			}
			work, err := store.FindWork(ctx, command.ID)
			if err != nil {
				return change, err
			}
			change.LifecycleChange = lifecycleOf(work)
			if work.Status == StatusDraft {
				return change, nil
			}
			now := dbtime.Now()
			if err := store.SetStatus(ctx, command.ID, StatusDraft, nil, now); err != nil {
				return change, err
			}
			change.Status = StatusDraft
			change.UpdatedAt = now
			change.Changed = true
			return change, nil
		},
		Audit:  workLifecycleChange.audit,
		Outbox: publishLifecycleSignal,
	})
	if err != nil {
		return LifecycleChange{}, err
	}
	return change.LifecycleChange, nil
}

// requireStoredWork answers a missing Work before the mutation locks it.
func (s *Service) requireStoredWork(ctx context.Context, workID string) error {
	return s.read(ctx, func(store Store) error { return requireWorkExists(ctx, store, workID) })
}

func requireWorkExists(ctx context.Context, store Store, workID string) error {
	exists, err := store.WorkExists(ctx, workID)
	if err != nil {
		return err
	}
	if !exists {
		return domainerr.ResourceNotFound("work", workID)
	}
	return nil
}

// FeaturedImageChange is the outcome of setting a featured image.
type FeaturedImageChange struct {
	ImageAsset        *commonv1.AssetRef
	OgGenerationRunID *string
}

// workFeaturedImageChange is the outcome of a featured image mutation.
type workFeaturedImageChange struct {
	fileID     string
	operation  sharedtelemetry.AuditCollectionOperation
	imageAsset *commonv1.AssetRef
	changed    bool
}

func (s *Service) featuredImageSpec(
	workID string,
	ogReason string,
	ogRunID **string,
	change func(context.Context, Store) (workFeaturedImageChange, error),
) mutation.StoreSpec[workFeaturedImageChange, Store] {
	return mutation.StoreSpec[workFeaturedImageChange, Store]{
		Access: lockedWork(workID, policyv1.Work.Edit, nil),
		Change: change,
		Audit: func(change workFeaturedImageChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkFeaturedImageAuditRecord(metadata, workID, change.fileID, change.operation)
			})
		},
		Outbox: func(ctx context.Context, store Store, change workFeaturedImageChange) error {
			if !change.changed {
				return nil
			}
			var err error
			if *ogRunID, err = requestWorkOG(ctx, store, workID, "", ogReason); err != nil {
				return err
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{WorkID: workID, Kind: MediaUpdate})
		},
	}
}

// SetWorkFeaturedImage sets the Work featured image (Site Admin-only).
func (s *Service) SetWorkFeaturedImage(ctx context.Context, command SetWorkFeaturedImageCommand) (FeaturedImageChange, error) {
	if err := s.requireStoredWork(ctx, command.WorkID); err != nil {
		return FeaturedImageChange{}, err
	}
	var ogRunID *string
	change, err := mutation.Run(ctx, s.mutations, s.featuredImageSpec(command.WorkID, "work_featured_image_updated", &ogRunID,
		func(ctx context.Context, store Store) (workFeaturedImageChange, error) {
			change := workFeaturedImageChange{fileID: command.FileID, operation: sharedtelemetry.AuditCollectionOperationAdded}
			current, err := store.FeaturedImageFileID(ctx, command.WorkID)
			if err != nil {
				return change, err
			}
			if current != nil && *current == command.FileID {
				return change, nil
			}
			if err := store.LockAttachableFile(ctx, command.FileID); err != nil {
				return change, err
			}
			fileID, err := store.FileID(ctx, command.FileID)
			if err != nil {
				return change, err
			}
			if err := store.SetFeaturedImage(ctx, command.WorkID, &command.FileID, time.Now()); err != nil {
				return change, err
			}
			change.imageAsset, err = store.BindFeaturedImage(ctx, command.WorkID, fileID)
			change.changed = err == nil
			return change, err
		},
	))
	if errors.Is(err, ErrFileNotFound) || errors.Is(err, ErrWorkNotFound) {
		return FeaturedImageChange{}, domainerr.ResourceNotFound("file", command.FileID)
	}
	if err != nil {
		return FeaturedImageChange{}, err
	}
	return FeaturedImageChange{ImageAsset: change.imageAsset, OgGenerationRunID: ogRunID}, nil
}

// DeleteWorkFeaturedImage removes the Work featured image (Site Admin-only)
// and returns the OG generation it started, if any.
func (s *Service) DeleteWorkFeaturedImage(ctx context.Context, command DeleteWorkFeaturedImageCommand) (*string, error) {
	var ogRunID *string
	_, err := mutation.Run(ctx, s.mutations, s.featuredImageSpec(command.WorkID, "work_featured_image_removed", &ogRunID,
		func(ctx context.Context, store Store) (workFeaturedImageChange, error) {
			change := workFeaturedImageChange{operation: sharedtelemetry.AuditCollectionOperationRemoved}
			current, err := store.FeaturedImageFileID(ctx, command.WorkID)
			if err != nil {
				return change, err
			}
			if current == nil {
				return change, nil
			}
			change.fileID = *current
			if err := store.SetFeaturedImage(ctx, command.WorkID, nil, time.Now()); err != nil {
				return change, err
			}
			err = store.ReleaseFeaturedImage(ctx, command.WorkID)
			change.changed = err == nil
			return change, err
		},
	))
	if errors.Is(err, ErrWorkNotFound) {
		return nil, domainerr.ResourceNotFound("work", command.WorkID)
	}
	if err != nil {
		return nil, err
	}
	return ogRunID, nil
}
