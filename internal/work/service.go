package work

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

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
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workSortConfig defines allowed sort fields for works
var workSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
		"title":        WorkSourceTitleSQL("work"),
		"status":       "status",
		"slug":         "slug",
		"type":         "type",
		"featured":     "featured",
	},
	DefaultSort: "created_at DESC",
}

var myCreditedWorkSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":      WorkSourceTitleSQL("w"),
		"type":       "w.type",
		"status":     "w.status",
		"created_at": "w.created_at",
		"updated_at": "w.updated_at",
	},
	DefaultSort: "w.created_at DESC, wc.id DESC",
}

var myCreditedWorkFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{WorkSourceTitleSQL("w")},
		},
		"title": {
			Column:     WorkSourceTitleSQL("w"),
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"type": {
			Column:     "w.type",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.WorkType_WORK_TYPE_MUSIC_PROJECT.String(),
				managev1.WorkType_WORK_TYPE_PORTFOLIO.String(),
				managev1.WorkType_WORK_TYPE_ARTICLE.String(),
				managev1.WorkType_WORK_TYPE_CONTRIBUTION.String(),
			},
		},
		"status": {
			Column:     "w.status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: workStatusValues(),
		},
	},
}

// ArtistSourceTitleSQL is the read projection used by Work credit views.
func ArtistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return "COALESCE((SELECT at.title FROM artist_translation AS at WHERE at.entity_id = " + alias + ".id AND at.locale = " + alias + ".source_locale LIMIT 1), '')"
}

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

// Deps are the collaborators of the Work use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Runtime requests OG images and binds Work media.
	Runtime Runtime
	// Signals schedules content.updated in the mutation's transaction.
	Signals       contentsignal.Publisher
	ContentBlocks *contentblock.Store
	Media         AuthorizedContentBlockMediaHydrator
	Members       MemberSummaryLoader
	// Checkpoints authorize the contributors of a collaboration save.
	Checkpoints persistencecheckpoint.ContributorFence
	// ShareLinks removes the share links of a deleted Work.
	ShareLinks ShareLinkCleanup
	Audit      domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Runtime": d.Runtime == nil, "Signals": d.Signals == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Media": d.Media == nil, "Members": d.Members == nil,
		"Checkpoints": d.Checkpoints == nil, "ShareLinks": d.ShareLinks == nil, "Audit": d.Audit == nil,
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
	db            *gorm.DB
	spiceDB       *auth.SpiceDBClient
	runtime       Runtime
	signals       contentsignal.Publisher
	contentBlocks *contentblock.Store
	mediaHydrator AuthorizedContentBlockMediaHydrator
	members       MemberSummaryLoader
	checkpoints   persistencecheckpoint.ContributorFence
	shareLinks    ShareLinkCleanup
	mutations     *mutation.Runner
}

// NewService validates deps and returns the Work use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, runtime: deps.Runtime, signals: deps.Signals,
		contentBlocks: deps.ContentBlocks, mediaHydrator: deps.Media, members: deps.Members,
		checkpoints: deps.Checkpoints, shareLinks: deps.ShareLinks, mutations: mutations,
	}, nil
}

// Page selects a page of a list. A zero Limit reads the list's default.
type Page struct {
	Limit  int32
	Offset int32
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

// GetWork retrieves a Work for the Site Admin management surface.
func (s *Service) GetWork(ctx context.Context, query GetWorkQuery) (*managev1.Work, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).Where("id = ?", query.ID).First(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("work", query.ID)
		}
		return nil, err
	}
	if err := s.requireWorkViewOrNotFound(ctx, work); err != nil {
		return nil, err
	}
	sourceState, err := loadWorkSourceLocaleDocumentState(ctx, s.db, work.ID)
	if err != nil {
		return nil, err
	}
	overlayWorkSourceLocaleDocument(&work, sourceState)
	if err := s.hydrateWorkContentProjection(ctx, &work); err != nil {
		return nil, err
	}
	imageAsset := s.getWorkFeaturedImageAsset(ctx, work.ID)
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, work.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoWork := s.toProtoWork(ctx, &work, imageAsset, ogAsset)
	protoWork.Clients, err = s.getWorkClients(ctx, work.ID)
	if err != nil {
		return nil, err
	}
	return protoWork, nil
}

// ListWorksAdmin returns a page of every Work with its credit and client
// counts (Site Admin-only).
func (s *Service) ListWorksAdmin(ctx context.Context, query ListWorksAdminQuery) (*managev1.ListWorksAdminResponse, error) {
	if err := requireWorkList(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	stored, err := WorkAdminFilterConfig.ApplyFilters(applyWorkPresenceFilters(s.db.WithContext(ctx).Model(&model.Work{}), &query.Filters), query.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, err
	}
	pg := queryutil.ExtractPaginationRaw(query.Page.Limit, query.Page.Offset, queryutil.DefaultPaginationConfig)
	stored, err = workSortConfig.ApplySort(stored, query.Sorts)
	if err != nil {
		return nil, err
	}
	var works []model.Work
	if err := stored.Order("work.id ASC").Limit(int(pg.Limit)).Offset(int(pg.Offset)).Find(&works).Error; err != nil {
		return nil, err
	}
	sourceStates, err := loadWorkSourceLocaleDocumentStates(ctx, s.db, collectManageWorkIDs(works))
	if err != nil {
		return nil, err
	}
	for i := range works {
		overlayWorkSourceLocaleDocument(&works[i], sourceStates[works[i].ID])
	}
	if err := s.hydrateWorkContentProjections(ctx, works); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyWorkOgAssets(ctx, works)
	if err != nil {
		return nil, err
	}
	workIDs := collectManageWorkIDs(works)
	creditCounts := s.countWorkRows(ctx, "work_credit", workIDs, "count_work_credits",
		"Work credit count lookup failed; listing works without credit counts")
	clientCounts := s.countWorkRows(ctx, "work_client", workIDs, "count_work_clients",
		"Work client count lookup failed; listing works without client counts")
	protoWorks := make([]*managev1.WorkWithStats, len(works))
	for i := range works {
		imageAsset := s.getWorkFeaturedImageAsset(ctx, works[i].ID)
		protoWorks[i] = &managev1.WorkWithStats{
			Work:        s.toProtoWork(ctx, &works[i], imageAsset, manageOgAssetFromReadyMap(readyOgAssets, works[i].OgAssetID)),
			CreditCount: creditCounts[works[i].ID],
			ClientCount: clientCounts[works[i].ID],
		}
	}
	return &managev1.ListWorksAdminResponse{Works: protoWorks, Pagination: pg.BuildResponse(total)}, nil
}

// applyWorkPresenceFilters applies has_slug and has_featured_image, which the
// filter configuration does not describe, and leaves the other filters.
func applyWorkPresenceFilters(stored *gorm.DB, filters *[]*commonv1.FilterSpec) *gorm.DB {
	presence := map[string][2]string{
		"has_slug":           {"slug IS NOT NULL AND slug != ''", "slug IS NULL OR slug = ''"},
		"has_featured_image": {"featured_image_file_id IS NOT NULL", "featured_image_file_id IS NULL"},
	}
	var standard []*commonv1.FilterSpec
	for _, filter := range *filters {
		if filter == nil {
			continue
		}
		conditions, ok := presence[filter.GetField()]
		switch {
		case !ok:
			standard = append(standard, filter)
		case filter.GetValue() == "true":
			stored = stored.Where(conditions[0])
		case filter.GetValue() == "false":
			stored = stored.Where(conditions[1])
		}
	}
	*filters = standard
	return stored
}

// countWorkRows counts the rows of table per Work. A failed count is
// logged and listed as zero.
func (s *Service) countWorkRows(ctx context.Context, table string, workIDs []string, operation, message string) map[string]int32 {
	counts := make(map[string]int32)
	if len(workIDs) == 0 {
		return counts
	}
	var stats []struct {
		WorkID string
		Count  int32
	}
	if err := s.db.WithContext(ctx).
		Table(table).
		Select("work_id, COUNT(*) as count").
		Where("work_id IN ?", workIDs).
		Group("work_id").
		Scan(&stats).Error; err != nil {
		slog.ErrorContext(ctx, message, "domain", "work", "operation", operation, failurecause.Attr(err))
	}
	for _, stat := range stats {
		counts[stat.WorkID] = stat.Count
	}
	return counts
}

// CreateWork creates a draft Work (Site Admin-only).
func (s *Service) CreateWork(ctx context.Context, command CreateWorkCommand) (*managev1.Work, error) {
	title := strings.TrimSpace(command.Title)
	work, err := s.newWorkFromCreateCommand(ctx, command, title)
	if err != nil {
		return nil, err
	}
	created, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.Work]{
		Access: mutation.FreshAdminCan(policyv1.Work.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Work, error) {
			return work, s.createWorkWithDB(ctx, tx, &work, title, command)
		},
		Audit: func(work model.Work) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditWorkCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkCreatedAuditRecord(metadata, work.ID)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, work model.Work) error {
			_, err := s.requestWorkOG(ctx, tx, work.ID, "", "work_created")
			return err
		},
		Relationships: workPolicyRelationships(true),
	})
	if err != nil {
		if !mutation.IsClassified(err) && dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("work", "slug", "slug").WithCause(err)
		}
		return nil, err
	}
	loadedWork, err := s.loadCreatedWork(ctx, created.ID)
	if err != nil {
		return nil, err
	}
	return s.workResponseWithReadyOg(ctx, &loadedWork, nil)
}

// workPolicyRelationships returns the Work's policy relationship batch:
// touched when the Work is created, deleted when it is deleted, each with
// its exact inverse.
func workPolicyRelationships(created bool) func(model.Work) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	return func(work model.Work) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
) (model.Work, error) {
	if title == "" {
		return model.Work{}, domainerr.Required("title")
	}
	if command.Type == "" {
		return model.Work{}, domainerr.Required("type")
	}
	if command.Year == 0 {
		return model.Work{}, domainerr.Required("year")
	}
	if command.Month == 0 {
		return model.Work{}, domainerr.Required("month")
	}
	isPresent := command.IsPresent != nil && *command.IsPresent
	untilYear, untilMonth := command.UntilYear, command.UntilMonth
	if isPresent {
		untilYear, untilMonth = nil, nil
	}
	if err := validateWorkRange(command.Year, command.Month, untilYear, untilMonth, isPresent); err != nil {
		return model.Work{}, err
	}
	slug, present := normalizeOptionalNullableString(command.Slug)
	if err := s.validateNewWorkSlug(ctx, slug); err != nil {
		return model.Work{}, err
	}
	work := model.Work{
		Type: command.Type, Year: command.Year, Month: command.Month,
		UntilYear: untilYear, UntilMonth: untilMonth, IsPresent: isPresent,
		Status: model.WorkStatusDraft,
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

func (s *Service) validateNewWorkSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := pathslug.Validate(*slug); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "work", "works", *slug)
}

func (s *Service) createWorkWithDB(
	ctx context.Context,
	tx *gorm.DB,
	work *model.Work,
	title string,
	command CreateWorkCommand,
) error {
	if work.Slug != nil && *work.Slug != "" {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "work", "works", *work.Slug); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	sourceLocale, err := resolveCreateSourceLocale(ctx, tx, command.SourceLocale, command.AcceptLanguage)
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
	createdDocument, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      workContentDocumentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	documentID := createdDocument.Document.ID.String()
	work.ContentDocumentID = &documentID
	if err := tx.Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"},
	}}).Create(work).Error; err != nil {
		return err
	}
	if command.Document != nil {
		replace, err := contentblock.ReplaceFromRichTextProto(createdDocument.Document.ID, createdDocument.Document.Revision, command.Document)
		if err != nil {
			return normalizeWorkContentBlockError(err)
		}
		if _, err := s.contentBlocks.ReplaceSnapshot(ctx, tx, replace, workContentCreationFence(work.ID, sourceLocale)); err != nil {
			return normalizeWorkContentBlockError(err)
		}
	}
	return createInitialWorkSourceLocaleMetadata(ctx, tx, work.ID, sourceLocale, title, command.Summary, now)
}

func (s *Service) loadCreatedWork(ctx context.Context, workID string) (model.Work, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", workID).Error; err != nil {
		return model.Work{}, err
	}
	sourceState, err := loadWorkSourceLocaleDocumentState(ctx, s.db, work.ID)
	if err != nil {
		return model.Work{}, err
	}
	overlayWorkSourceLocaleDocument(&work, sourceState)
	return work, nil
}

// DeleteWork deletes a Work after it has left the Archived lifecycle state
// (Site Admin-only).
func (s *Service) DeleteWork(ctx context.Context, command DeleteWorkCommand) error {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("work", command.ID)
		}
		return err
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, work.ID)
	if err != nil {
		return err
	}
	var locked model.Work
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[model.Work]{
		Access: lockedWork(work.ID, policyv1.Work.Delete, &locked),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Work, error) {
			if locked.Status == model.WorkStatusArchived {
				return work, domainerr.Precondition("archived works must be published before deletion")
			}
			return work, s.deleteWorkWithDB(ctx, tx, work, documentID)
		},
		Audit: func(work model.Work) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditWorkDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkDeletedAuditRecord(metadata, work.ID)
			})
		},
		Relationships: workPolicyRelationships(false),
	})
	return err
}

func (s *Service) deleteWorkWithDB(ctx context.Context, tx *gorm.DB, work model.Work, documentID uuid.UUID) error {
	if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, lockedWorkContentFence()); err != nil {
		return err
	}
	if err := s.shareLinks.DeleteEntityShareLinks(ctx, tx, work.ID); err != nil {
		return err
	}
	if err := s.runtime.CancelAndReleaseEntityWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, "work", work.ID); err != nil {
		return err
	}
	if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "work", work.ID, "featured_image"); err != nil {
		return err
	}
	return tx.Delete(&work).Error
}

// workLifecycleChange is the outcome of a publish or unpublish.
type workLifecycleChange struct {
	work      model.Work
	previous  sharedtelemetry.AuditState
	next      sharedtelemetry.AuditState
	published bool
	changed   bool
}

func (change workLifecycleChange) audit() mutation.Audit {
	if !change.changed {
		return mutation.Unchanged()
	}
	return workUpdatedAudit(func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewWorkLifecycleAuditRecord(metadata, change.work.ID, change.previous, change.next)
	})
}

func (s *Service) publishLifecycleSignal(ctx context.Context, tx *gorm.DB, change workLifecycleChange) error {
	if !change.changed {
		return nil
	}
	fields := []string{"state.status"}
	if change.published {
		fields = append(fields, "state.published_at")
	}
	return contentsignal.PublishInTransaction(ctx, tx, s.signals, buildManageStateTransitionContentUpdatedEvent(change.work.ID, fields))
}

func workLifecycleResponse(change workLifecycleChange) *managev1.WorkLifecycleMutationResponse {
	work := change.work
	return &managev1.WorkLifecycleMutationResponse{
		Id: work.ID, Changed: change.changed,
		Status:      workStatusToProto(work.Status),
		PublishedAt: timestampProtoPtr(work.PublishedAt), UpdatedAt: timestamppb.New(work.UpdatedAt),
	}
}

// PublishWork publishes a draft Work or restores an archived one (Site
// Admin-only).
func (s *Service) PublishWork(ctx context.Context, command PublishWorkCommand) (*managev1.WorkLifecycleMutationResponse, error) {
	if err := requireStoredWork(ctx, s.db, command.ID); err != nil {
		return nil, err
	}
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[workLifecycleChange]{
		Access: lockedWork(command.ID, policyv1.Work.Publish, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (workLifecycleChange, error) {
			change := workLifecycleChange{next: sharedtelemetry.AuditStatePublished}
			if err := tx.First(&change.work, "id = ?", command.ID).Error; err != nil {
				return change, err
			}
			if change.work.Status == model.WorkStatusPublished {
				return change, nil
			}
			now := dbtime.Now()
			updates := structured.Fields{"status": model.WorkStatusPublished, "updated_at": now}
			change.previous = sharedtelemetry.AuditStateDraft
			if change.work.Status == model.WorkStatusArchived {
				change.previous = sharedtelemetry.AuditStateArchived
			} else {
				updates["published_at"] = now
				change.published = true
			}
			if err := tx.Model(&change.work).Updates(updates).Error; err != nil {
				return change, err
			}
			change.work.Status = model.WorkStatusPublished
			change.work.UpdatedAt = now
			if change.published {
				change.work.PublishedAt = &now
			}
			change.changed = true
			return change, nil
		},
		Audit:  workLifecycleChange.audit,
		Outbox: s.publishLifecycleSignal,
	})
	if err != nil {
		return nil, err
	}
	return workLifecycleResponse(change), nil
}

// UnpublishWork moves a published Work back to draft (Site Admin-only).
func (s *Service) UnpublishWork(ctx context.Context, command UnpublishWorkCommand) (*managev1.WorkLifecycleMutationResponse, error) {
	if err := requireStoredWork(ctx, s.db, command.ID); err != nil {
		return nil, err
	}
	var locked model.Work
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[workLifecycleChange]{
		Access: lockedWork(command.ID, policyv1.Work.Publish, &locked),
		Change: func(ctx context.Context, tx *gorm.DB) (workLifecycleChange, error) {
			change := workLifecycleChange{previous: sharedtelemetry.AuditStatePublished, next: sharedtelemetry.AuditStateDraft}
			if locked.Status == model.WorkStatusArchived {
				return change, domainerr.Precondition("archived works must be published before they can be unpublished")
			}
			if err := tx.First(&change.work, "id = ?", command.ID).Error; err != nil {
				return change, err
			}
			if change.work.Status == model.WorkStatusDraft {
				return change, nil
			}
			now := dbtime.Now()
			if err := tx.Model(&change.work).Updates(structured.Fields{"status": model.WorkStatusDraft, "updated_at": now}).Error; err != nil {
				return change, err
			}
			change.work.Status = model.WorkStatusDraft
			change.work.UpdatedAt = now
			change.changed = true
			return change, nil
		},
		Audit:  workLifecycleChange.audit,
		Outbox: s.publishLifecycleSignal,
	})
	if err != nil {
		return nil, err
	}
	return workLifecycleResponse(change), nil
}

// requireStoredWork answers a missing Work before the mutation locks it.
func requireStoredWork(ctx context.Context, db *gorm.DB, workID string) error {
	var work model.Work
	if err := db.WithContext(ctx).Select("id").First(&work, "id = ?", workID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("work", workID)
		}
		return err
	}
	return nil
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
	change func(context.Context, *gorm.DB) (workFeaturedImageChange, error),
) mutation.Spec[workFeaturedImageChange] {
	return mutation.Spec[workFeaturedImageChange]{
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
		Outbox: func(ctx context.Context, tx *gorm.DB, change workFeaturedImageChange) error {
			if !change.changed {
				return nil
			}
			var err error
			if *ogRunID, err = s.requestWorkOG(ctx, tx, workID, "", ogReason); err != nil {
				return err
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.signals, buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, workID, "media.featured_image",
			))
		},
	}
}

// SetWorkFeaturedImage sets the Work featured image (Site Admin-only).
func (s *Service) SetWorkFeaturedImage(ctx context.Context, command SetWorkFeaturedImageCommand) (*managev1.SetWorkFeaturedImageResponse, error) {
	if err := requireStoredWork(ctx, s.db, command.WorkID); err != nil {
		return nil, err
	}
	var ogRunID *string
	change, err := mutation.Run(ctx, s.mutations, s.featuredImageSpec(command.WorkID, "work_featured_image_updated", &ogRunID,
		func(ctx context.Context, tx *gorm.DB) (workFeaturedImageChange, error) {
			change := workFeaturedImageChange{fileID: command.FileID, operation: sharedtelemetry.AuditCollectionOperationAdded}
			var current model.Work
			if err := tx.Select("id", "featured_image_file_id", "status").First(&current, "id = ?", command.WorkID).Error; err != nil {
				return change, err
			}
			if current.FeaturedImageFileID != nil && *current.FeaturedImageFileID == command.FileID {
				return change, nil
			}
			if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{command.FileID}); err != nil {
				return change, err
			}
			var file model.File
			if err := tx.First(&file, "id = ?", command.FileID).Error; err != nil {
				return change, err
			}
			if err := tx.Model(&model.Work{}).Where("id = ?", command.WorkID).Updates(structured.Fields{
				"featured_image_file_id": command.FileID,
				"updated_at":             time.Now(),
			}).Error; err != nil {
				return change, err
			}
			var err error
			change.imageAsset, err = s.runtime.BindReadyAssetForSourceFile(ctx, tx, file.ID, "work", command.WorkID, "featured_image", "image")
			change.changed = err == nil
			return change, err
		},
	))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound("file", command.FileID)
	}
	if err != nil {
		return nil, err
	}
	return &managev1.SetWorkFeaturedImageResponse{ImageAsset: change.imageAsset, OgGenerationRunId: ogRunID}, nil
}

// DeleteWorkFeaturedImage removes the Work featured image (Site Admin-only).
func (s *Service) DeleteWorkFeaturedImage(ctx context.Context, command DeleteWorkFeaturedImageCommand) (*managev1.OgAssetDeleteResponse, error) {
	var ogRunID *string
	_, err := mutation.Run(ctx, s.mutations, s.featuredImageSpec(command.WorkID, "work_featured_image_removed", &ogRunID,
		func(ctx context.Context, tx *gorm.DB) (workFeaturedImageChange, error) {
			change := workFeaturedImageChange{operation: sharedtelemetry.AuditCollectionOperationRemoved}
			var current struct {
				FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
			}
			if err := tx.Table("work").Select("featured_image_file_id").Where("id = ?", command.WorkID).Take(&current).Error; err != nil {
				return change, err
			}
			if current.FeaturedImageFileID == nil {
				return change, nil
			}
			change.fileID = *current.FeaturedImageFileID
			result := tx.Model(&model.Work{}).Where("id = ?", command.WorkID).Updates(structured.Fields{
				"featured_image_file_id": nil,
				"updated_at":             time.Now(),
			})
			if result.Error != nil {
				return change, result.Error
			}
			if result.RowsAffected == 0 {
				return change, gorm.ErrRecordNotFound
			}
			err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "work", command.WorkID, "featured_image")
			change.changed = err == nil
			return change, err
		},
	))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound("work", command.WorkID)
	}
	if err != nil {
		return nil, err
	}
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: ogRunID}, nil
}
