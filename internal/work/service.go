package work

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
			EnumValues: []string{
				managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
				managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
				managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
			},
		},
	},
}

// ArtistSourceTitleSQL is the read projection used by Work credit views.
func ArtistSourceTitleSQL(tableAlias string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return "COALESCE((SELECT at.title FROM artist_translation AS at JOIN artist AS source ON source.id = at.entity_id AND source.source_locale = at.locale WHERE at.id = " + alias + ".id LIMIT 1), '')"
}

func validateWorkDatePoint(yearField, monthField string, year, month int32) error {
	if year < 1 || year > 9999 {
		return errs.InvalidArgument(yearField, "must be between 1 and 9999")
	}
	if month < 1 || month > 12 {
		return errs.InvalidArgument(monthField, "must be between 1 and 12")
	}
	return nil
}

func validateWorkRange(year, month int32, untilYear, untilMonth *int32, isPresent bool) error {
	if err := validateWorkDatePoint("year", "month", year, month); err != nil {
		return err
	}

	if isPresent {
		if untilYear != nil || untilMonth != nil {
			return errs.InvalidArgument("until", "must be empty when is_present is true")
		}
		return nil
	}

	if untilYear == nil {
		return errs.Required("until_year")
	}
	if untilMonth == nil {
		return errs.Required("until_month")
	}
	if err := validateWorkDatePoint("until_year", "until_month", *untilYear, *untilMonth); err != nil {
		return err
	}
	if *untilYear < year || (*untilYear == year && *untilMonth < month) {
		return errs.InvalidArgument("until", "must not be earlier than from")
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

// WorkService implements the WorkService Connect handler
type WorkService struct {
	managev1connect.UnimplementedWorkServiceHandler
	db             *gorm.DB
	runtime        Runtime
	spiceDB        *auth.SpiceDBClient
	kratosClient   auth.IdentityManager
	asyncPublisher AsyncPublisher
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	mediaHydrator  AuthorizedContentBlockMediaHydrator
	members        MemberSummaryLoader
}

type WorkServiceOption func(*WorkService)

func WithWorkContentBlockStore(store *contentblock.Store) WorkServiceOption {
	return func(s *WorkService) {
		s.contentBlocks = store
	}
}

func WithWorkContentBlockMediaHydrator(hydrator AuthorizedContentBlockMediaHydrator) WorkServiceOption {
	return func(s *WorkService) {
		s.mediaHydrator = hydrator
	}
}

func WithWorkMemberSummaryLoader(loader MemberSummaryLoader) WorkServiceOption {
	return func(s *WorkService) { s.members = loader }
}

// NewWorkService creates a new WorkService
func NewWorkService(db *gorm.DB, runtime Runtime, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, asyncPublisher AsyncPublisher, options ...WorkServiceOption) *WorkService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	if kratosClient == nil {
		panic("kratosClient is required")
	}
	if asyncPublisher == nil {
		panic("asyncPublisher is required")
	}
	if runtime == nil {
		panic("Work runtime is required")
	}
	service := &WorkService{
		db:             db,
		runtime:        runtime,
		spiceDB:        spiceDB,
		kratosClient:   kratosClient,
		asyncPublisher: asyncPublisher,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func NewAuditedWorkService(
	db *gorm.DB,
	runtime Runtime,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	asyncPublisher AsyncPublisher,
	auditWriter domainaudit.Appender,
	options ...WorkServiceOption,
) *WorkService {
	if auditWriter == nil {
		panic("work audit writer is required")
	}
	service := NewWorkService(db, runtime, spiceDB, kratosClient, asyncPublisher, options...)
	service.auditWriter = auditWriter
	return service
}

// =============================================================================
// Read Methods (the management surface is Site Admin-only)
// =============================================================================

// GetWork retrieves a Work for the Site Admin management surface.
func (s *WorkService) GetWork(
	ctx context.Context,
	req *connect.Request[managev1.GetWorkRequest],
) (*connect.Response[managev1.Work], error) {
	var work model.Work
	if err := s.db.WithContext(ctx).
		Where("id = ?", req.Msg.Id).
		First(&work).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := s.requireWorkViewOrNotFound(ctx, work); err != nil {
		return nil, err
	}
	if sourceState, err := loadWorkSourceLocaleDocumentState(ctx, s.db, work.ID); err != nil {
		return nil, err
	} else {
		overlayWorkSourceLocaleDocument(&work, sourceState)
	}
	if err := s.hydrateWorkContentProjection(ctx, &work); err != nil {
		return nil, err
	}

	imageAsset := s.getWorkFeaturedImageAsset(ctx, work.ID)
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, work.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoWork := s.toProtoWork(&work, imageAsset, ogAsset)
	protoWork.Clients = s.getWorkClients(ctx, work.ID)
	return connect.NewResponse(protoWork), nil
}

// GetWorkCredits returns Work attribution for the Site Admin management surface.
func (s *WorkService) ListWorksAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListWorksAdminRequest],
) (*connect.Response[managev1.ListWorksAdminResponse], error) {
	// Check admin role
	if err := requireWorkList(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	var works []model.Work
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Work{})

	// Handle special presence filters (has_slug, has_featured_image) before ApplyFilters
	// These are not in FilterConfig, so we process them separately
	var standardFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		switch f.GetField() {
		case "has_slug":
			if f.GetValue() == "true" {
				query = query.Where("slug IS NOT NULL AND slug != ''")
			} else if f.GetValue() == "false" {
				query = query.Where("slug IS NULL OR slug = ''")
			}
		case "has_featured_image":
			if f.GetValue() == "true" {
				query = query.Where("featured_image_file_id IS NOT NULL")
			} else if f.GetValue() == "false" {
				query = query.Where("featured_image_file_id IS NULL")
			}
		default:
			standardFilters = append(standardFilters, f)
		}
	}

	// Apply standard filters using FilterConfig
	var err error
	query, err = WorkAdminFilterConfig.ApplyFilters(query, standardFilters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	pg := queryutil.ExtractPagination(req.Msg.Pagination, queryutil.DefaultPaginationConfig)

	// Apply sorting
	query, err = workSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(pg.Limit)).Offset(int(pg.Offset)).Find(&works).Error; err != nil {
		return nil, errs.Internal(err)
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

	// Get stats for each work
	workIDs := make([]string, len(works))
	for i, w := range works {
		workIDs[i] = w.ID
	}

	// Get credit counts
	creditCounts := make(map[string]int32)
	if len(workIDs) > 0 {
		var creditStats []struct {
			WorkID string
			Count  int32
		}
		if err := s.db.WithContext(ctx).
			Table("work_credit").
			Select("work_id, COUNT(*) as count").
			Where("work_id IN ?", workIDs).
			Group("work_id").
			Scan(&creditStats).Error; err != nil {
			slog.Warn("Failed to get credit counts", "error", err)
		}
		for _, stat := range creditStats {
			creditCounts[stat.WorkID] = stat.Count
		}
	}

	// Get client counts
	clientCounts := make(map[string]int32)
	if len(workIDs) > 0 {
		var clientStats []struct {
			WorkID string
			Count  int32
		}
		if err := s.db.WithContext(ctx).
			Table("work_client").
			Select("work_id, COUNT(*) as count").
			Where("work_id IN ?", workIDs).
			Group("work_id").
			Scan(&clientStats).Error; err != nil {
			slog.Warn("Failed to get client counts", "error", err)
		}
		for _, stat := range clientStats {
			clientCounts[stat.WorkID] = stat.Count
		}
	}

	// Convert to proto with stats
	protoWorks := make([]*managev1.WorkWithStats, len(works))
	for i, work := range works {
		imageAsset := s.getWorkFeaturedImageAsset(ctx, work.ID)
		protoWorks[i] = &managev1.WorkWithStats{
			Work:        s.toProtoWork(&work, imageAsset, manageOgAssetFromReadyMap(readyOgAssets, work.OgAssetID)),
			CreditCount: creditCounts[work.ID],
			ClientCount: clientCounts[work.ID],
		}
	}

	return connect.NewResponse(&managev1.ListWorksAdminResponse{
		Works:      protoWorks,
		Pagination: pg.BuildResponse(total),
	}), nil
}

// CreateWork creates a new work (admin only)
func (s *WorkService) CreateWork(
	ctx context.Context,
	req *connect.Request[managev1.CreateWorkRequest],
) (*connect.Response[managev1.Work], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Work content Block store is not configured")
	}
	title := strings.TrimSpace(req.Msg.Title)
	work, err := s.newWorkFromCreateRequest(ctx, req.Msg, title)
	if err != nil {
		return nil, err
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		return s.createWorkWithDB(
			ctx, tx, &work, title, req.Msg.Summary, req.Msg.Document,
			req.Header().Get("Accept-Language"),
			write,
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, errs.SlugAlreadyExists("work", "slug")
		}
		return nil, errs.Internal(err)
	}
	loadedWork, err := s.loadCreatedWork(ctx, work.ID)
	if err != nil {
		return nil, err
	}
	return s.workResponseWithReadyOg(ctx, &loadedWork, nil)
}

func (s *WorkService) newWorkFromCreateRequest(
	ctx context.Context,
	request *managev1.CreateWorkRequest,
	title string,
) (model.Work, error) {
	if title == "" {
		return model.Work{}, errs.Required("title")
	}
	if request.Type == managev1.WorkType_WORK_TYPE_UNSPECIFIED {
		return model.Work{}, errs.Required("type")
	}
	if request.Year == 0 {
		return model.Work{}, errs.Required("year")
	}
	if request.Month == 0 {
		return model.Work{}, errs.Required("month")
	}
	isPresent := request.IsPresent != nil && *request.IsPresent
	untilYear, untilMonth := request.UntilYear, request.UntilMonth
	if isPresent {
		untilYear, untilMonth = nil, nil
	}
	if err := validateWorkRange(request.Year, request.Month, untilYear, untilMonth, isPresent); err != nil {
		return model.Work{}, err
	}
	slug, present := normalizeOptionalNullableString(request.Slug)
	if err := s.validateNewWorkSlug(ctx, slug); err != nil {
		return model.Work{}, err
	}
	work := model.Work{
		Type: request.Type.String(), Year: request.Year, Month: request.Month,
		UntilYear: untilYear, UntilMonth: untilMonth, IsPresent: isPresent,
		Status: managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
	}
	if present {
		work.Slug = slug
	}
	if request.Metadata != nil {
		work.Metadata = sanitizeWorkMetadata(request.Metadata.AsMap())
	}
	if request.Featured != nil {
		work.Featured = *request.Featured
	}
	return work, nil
}

func (s *WorkService) validateNewWorkSlug(ctx context.Context, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := validateSlugWithoutSlash(*slug); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "work", "works", *slug)
}

func (s *WorkService) createWorkWithDB(
	ctx context.Context,
	tx *gorm.DB,
	work *model.Work,
	title string,
	summary *string,
	document *contentv1.RichTextDocument,
	acceptLanguage string,
	write authzmutation.WriteRelationships,
) error {
	if err := requireLockedWorkCreate(ctx, tx, s.spiceDB); err != nil {
		return err
	}
	if work.Slug != nil && *work.Slug != "" {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "work", "works", *work.Slug); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	sourceLocale := resolveInitialSourceLocale(ctx, tx, s.kratosClient, acceptLanguage)
	work.SourceLocale = sourceLocale
	if document != nil {
		if document.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK {
			return errs.InvalidArgument("document.profile", "must be Work")
		}
		if document.GetSourceLocale() != sourceLocale {
			return errs.InvalidArgument("document.source_locale", "must match the server-selected source locale")
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
	if document != nil {
		replace, replaceErr := contentblock.ReplaceFromRichTextProto(
			createdDocument.Document.ID,
			createdDocument.Document.Revision,
			document,
		)
		if replaceErr != nil {
			return normalizeWorkContentBlockError(replaceErr)
		}
		if _, replaceErr = s.contentBlocks.ReplaceSnapshot(
			ctx,
			tx,
			replace,
			workContentCreationFence(work.ID, sourceLocale),
		); replaceErr != nil {
			return normalizeWorkContentBlockError(replaceErr)
		}
	}
	if err := createInitialWorkSourceLocaleMetadata(
		ctx, tx, work.ID, sourceLocale, title, summary, now,
	); err != nil {
		return err
	}
	_, err = s.runtime.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, work.ID, "", false, "work_created",
	)
	if err != nil {
		return err
	}
	touchPolicy, err := policyv1.Work.TouchPolicy(work.ID)
	if err != nil {
		return err
	}
	deletePolicy, err := policyv1.Work.DeletePolicy(work.ID)
	if err != nil {
		return err
	}
	if err := write(
		[]policyv1.RelationshipMutation{touchPolicy},
		[]policyv1.RelationshipMutation{deletePolicy},
	); err != nil {
		return err
	}
	if s.auditWriter != nil {
		if err := domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditWorkCreated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewWorkCreatedAuditRecord(metadata, work.ID)
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *WorkService) loadCreatedWork(ctx context.Context, workID string) (model.Work, error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", workID).Error; err != nil {
		return model.Work{}, errs.Internal(err)
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
func (s *WorkService) DeleteWork(
	ctx context.Context,
	req *connect.Request[managev1.DeleteWorkRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Work content Block store is not configured")
	}
	// First get the work to check featured image
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	documentID, err := loadWorkContentDocumentID(ctx, s.db, work.ID)
	if err != nil {
		return nil, err
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		lockedWork, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, work.ID, policyv1.Work.Delete, workAuthorizationMutation)
		if err != nil {
			return err
		}
		if lockedWork.Status == managev1.WorkStatus_WORK_STATUS_ARCHIVED.String() {
			return errs.FailedPrecondition("archived works must be published before deletion")
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			lockedWorkContentFence(),
		); err != nil {
			return err
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK.String(), req.Msg.Id).
			Delete(&model.ShareLink{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.runtime.CancelAndReleaseEntityWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			"work", work.ID,
		); err != nil {
			return err
		}
		if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "work", work.ID, "featured_image"); err != nil {
			return err
		}
		if err := tx.Delete(&work).Error; err != nil {
			return err
		}
		deletePolicy, err := policyv1.Work.DeletePolicy(work.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.Work.TouchPolicy(work.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		); err != nil {
			return err
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(
				ctx,
				tx,
				s.auditWriter,
				sharedtelemetry.AuditWorkDeleted,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewWorkDeletedAuditRecord(metadata, work.ID)
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
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// =============================================================================
// Publish/Unpublish (admin only)
// =============================================================================

// PublishWork publishes a work (Site Admin-only).
func (s *WorkService) PublishWork(
	ctx context.Context,
	req *connect.Request[managev1.PublishWorkRequest],
) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	changedFields := []string{"state.status"}
	lifecycleChanged := false
	publishedAtChanged := false
	var mutationNow time.Time
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, work.ID, policyv1.Work.Publish, workAuthorizationMutation); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&work, "id = ?", work.ID).Error; err != nil {
			return err
		}
		if work.Status == managev1.WorkStatus_WORK_STATUS_PUBLISHED.String() {
			return nil
		}
		now := time.Now()
		mutationNow = now
		updates := structured.Fields{"status": managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(), "updated_at": now}
		previous := sharedtelemetry.AuditStateDraft
		if work.Status == managev1.WorkStatus_WORK_STATUS_ARCHIVED.String() {
			previous = sharedtelemetry.AuditStateArchived
		} else {
			updates["published_at"] = now
			changedFields = append(changedFields, "state.published_at")
			publishedAtChanged = true
		}
		if err := tx.Model(&work).Updates(updates).Error; err != nil {
			return err
		}
		lifecycleChanged = true
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkLifecycleAuditRecord(metadata, work.ID, previous, sharedtelemetry.AuditStatePublished)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if lifecycleChanged {
		work.Status = managev1.WorkStatus_WORK_STATUS_PUBLISHED.String()
		work.UpdatedAt = mutationNow
		if publishedAtChanged {
			work.PublishedAt = &mutationNow
		}
	}
	if lifecycleChanged {
		publishWorkContentUpdated(
			ctx,
			s.asyncPublisher,
			buildManageStateTransitionContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
				work.ID,
				changedFields,
			),
		)
	}
	return connect.NewResponse(&managev1.WorkLifecycleMutationResponse{
		Id: work.ID, Changed: lifecycleChanged,
		Status:      managev1.WorkStatus(managev1.WorkStatus_value[work.Status]),
		PublishedAt: timestampProtoPtr(work.PublishedAt), UpdatedAt: timestamppb.New(work.UpdatedAt),
	}), nil
}

// UnpublishWork unpublishes a work (Site Admin-only).
func (s *WorkService) UnpublishWork(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishWorkRequest],
) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	lifecycleChanged := false
	var mutationNow time.Time
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockedWork, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, work.ID, policyv1.Work.Publish, workAuthorizationMutation)
		if err != nil {
			return err
		}
		if lockedWork.Status == managev1.WorkStatus_WORK_STATUS_ARCHIVED.String() {
			return errs.FailedPrecondition("archived works must be published before they can be unpublished")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&work, "id = ?", work.ID).Error; err != nil {
			return err
		}
		if work.Status == managev1.WorkStatus_WORK_STATUS_DRAFT.String() {
			return nil
		}
		mutationNow = time.Now().UTC()
		if err := tx.Model(&work).Updates(structured.Fields{"status": managev1.WorkStatus_WORK_STATUS_DRAFT.String(), "updated_at": mutationNow}).Error; err != nil {
			return err
		}
		lifecycleChanged = true
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkLifecycleAuditRecord(metadata, work.ID, sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft)
		})
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if lifecycleChanged {
		work.Status = managev1.WorkStatus_WORK_STATUS_DRAFT.String()
		work.UpdatedAt = mutationNow
	}
	if lifecycleChanged {
		publishWorkContentUpdated(
			ctx,
			s.asyncPublisher,
			buildManageStateTransitionContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
				work.ID,
				[]string{"state.status"},
			),
		)
	}
	return connect.NewResponse(&managev1.WorkLifecycleMutationResponse{
		Id: work.ID, Changed: lifecycleChanged,
		Status:      managev1.WorkStatus(managev1.WorkStatus_value[work.Status]),
		PublishedAt: timestampProtoPtr(work.PublishedAt), UpdatedAt: timestamppb.New(work.UpdatedAt),
	}), nil
}

// =============================================================================
// Featured Image Management (Site Admin-only)
// =============================================================================

// SetWorkFeaturedImage sets the work featured image
func (s *WorkService) SetWorkFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.SetWorkFeaturedImageRequest],
) (*connect.Response[managev1.SetWorkFeaturedImageResponse], error) {
	// Verify work exists.
	var work model.Work
	if err := s.db.WithContext(ctx).First(&work, "id = ?", req.Msg.WorkId).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, errs.Internal(err)
	}
	var imageAsset *commonv1.AssetRef
	var ogRunID *string
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Work
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "featured_image_file_id", "status").
			First(&current, "id = ?", req.Msg.WorkId).Error; err != nil {
			return err
		}
		if _, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, req.Msg.WorkId, policyv1.Work.Edit, workAuthorizationMutation); err != nil {
			return err
		}
		if current.FeaturedImageFileID != nil && *current.FeaturedImageFileID == req.Msg.FileId {
			return nil
		}
		if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}
		var file model.File
		if err := tx.First(&file, "id = ?", req.Msg.FileId).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Work{}).Where("id = ?", req.Msg.WorkId).Updates(structured.Fields{
			"featured_image_file_id": req.Msg.FileId,
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return err
		}
		changed = true
		imageRef, err := s.runtime.BindReadyAssetForSourceFile(
			ctx, tx, file.ID, "work", req.Msg.WorkId, "featured_image", "image",
		)
		if err != nil {
			return err
		}
		imageAsset = imageRef
		runID, err := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			req.Msg.WorkId, "", false, "work_featured_image_updated",
		)
		if err != nil {
			return err
		}
		if runID != "" {
			ogRunID = &runID
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFeaturedImageAuditRecord(metadata, req.Msg.WorkId, req.Msg.FileId, sharedtelemetry.AuditCollectionOperationAdded)
		})
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("file", req.Msg.FileId)
		}
		return nil, err
	}

	if changed {
		publishWorkContentUpdated(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
				work.ID,
				"media.featured_image",
			),
		)
	}

	return connect.NewResponse(&managev1.SetWorkFeaturedImageResponse{
		ImageAsset: imageAsset, OgGenerationRunId: ogRunID,
	}), nil
}

// DeleteWorkFeaturedImage deletes the work featured image
func (s *WorkService) DeleteWorkFeaturedImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteWorkFeaturedImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	var ogRunID *string
	changed := false

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current struct {
			FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
			Status              string  `gorm:"column:status"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("work").
			Select("featured_image_file_id", "status").
			Where("id = ?", req.Msg.WorkId).
			First(&current).Error; err != nil {
			return err
		}
		if _, err := requireLockedWorkPermission(ctx, tx, s.spiceDB, req.Msg.WorkId, policyv1.Work.Edit, workAuthorizationMutation); err != nil {
			return err
		}
		if current.FeaturedImageFileID == nil {
			return nil
		}
		result := tx.Model(&model.Work{}).
			Where("id = ?", req.Msg.WorkId).
			Updates(structured.Fields{
				"featured_image_file_id": nil,
				"updated_at":             time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		changed = true
		if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "work", req.Msg.WorkId, "featured_image"); err != nil {
			return err
		}
		runID, requestErr := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
			req.Msg.WorkId, "", false, "work_featured_image_removed",
		)
		if requestErr != nil {
			return requestErr
		}
		if runID != "" {
			ogRunID = &runID
		}
		return s.appendWorkAudit(ctx, tx, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewWorkFeaturedImageAuditRecord(metadata, req.Msg.WorkId, *current.FeaturedImageFileID, sharedtelemetry.AuditCollectionOperationRemoved)
		})
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("work", req.Msg.WorkId)
		}
		return nil, err
	}

	if changed {
		publishWorkContentUpdated(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
				req.Msg.WorkId,
				"media.featured_image",
			),
		)
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true, OgGenerationRunId: ogRunID,
	}), nil
}

// =============================================================================
// Credits Management (Site Admin-only; credit is attribution, not authority)
// =============================================================================

// CreateWorkCreditGroup creates a credit group for a work
