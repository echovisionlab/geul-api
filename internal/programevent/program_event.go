package programevent

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
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

const EntityType = "program_event"

var programEventSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"starts_at":    "starts_at",
		"published_at": "published_at",
		"updated_at":   "updated_at",
		"created_at":   "created_at",
	},
	DefaultSort: "starts_at DESC",
}

var programEventSeriesSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"updated_at": "updated_at",
		"created_at": "created_at",
		"title":      programEventSeriesSourceTitleSQL,
	},
	DefaultSort: "updated_at DESC",
}

var programEventTypeSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"sort_order": "sort_order",
		"slug":       "slug",
		"updated_at": "updated_at",
		"created_at": "created_at",
	},
	DefaultSort: "sort_order ASC, slug ASC",
}

var ProgramEventFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				programEventSourceTitleSQL,
			},
		},
		"slug": {
			Column:     "slug",
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.TextOps,
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
				managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
				managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(),
			},
		},
		"type_id": {
			Column:     "type_id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
			IsFK:       true,
		},
		"series_id": {
			Column:     "series_id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
			IsFK:       true,
		},
		"map_place_id": {
			Column: "map_place_id",
			Type:   queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{
				commonv1.FilterOp_FILTER_OP_EQ,
				commonv1.FilterOp_FILTER_OP_NEQ,
				commonv1.FilterOp_FILTER_OP_IN,
				commonv1.FilterOp_FILTER_OP_NOT_IN,
				commonv1.FilterOp_FILTER_OP_IS_NULL,
				commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
			},
			IsFK: true,
		},
		"starts_at": {
			Column:     "starts_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
		"published_at": {
			Column:     "published_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
	},
}

var ProgramEventSeriesFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				programEventSeriesSourceTitleSQL,
			},
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
				managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
			},
		},
	},
}

var ProgramEventTypeFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String(),
				managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE.String(),
			},
		},
	},
}

const (
	programEventSourceTitleSQL       = "program_event.title"
	programEventSeriesSourceTitleSQL = "program_event_series.title"
)

type ProgramEventService struct {
	managev1connect.UnimplementedProgramEventServiceHandler
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	asyncPublisher AsyncPublisher
	runtime        Runtime
	fileDeleter    FileDeleter
	creditMembers  CreditMemberSummaries
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
}

type ProgramEventServiceOption func(*ProgramEventService)

func WithProgramEventContentBlockStore(store *contentblock.Store) ProgramEventServiceOption {
	return func(service *ProgramEventService) {
		service.contentBlocks = store
	}
}

// WithProgramEventAsyncPublisher supplies the existing coalescible content
// update signal used by collaboration and other runtime consumers. DCDP
// writes fail closed before persistence when this dependency is absent.
func WithProgramEventAsyncPublisher(publisher AsyncPublisher) ProgramEventServiceOption {
	return func(service *ProgramEventService) {
		service.asyncPublisher = publisher
	}
}

func NewProgramEventService(
	db *gorm.DB,
	runtime Runtime,
	spiceDB *auth.SpiceDBClient,
	creditMembers CreditMemberSummaries,
	fileDeleter ...FileDeleter,
) *ProgramEventService {
	if db == nil {
		panic("db is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(runtime, "Program Event runtime")
	dependencycheck.MustNotNil(creditMembers, "creditMembers")
	var deleter FileDeleter
	if len(fileDeleter) > 0 {
		deleter = fileDeleter[0]
	}
	return &ProgramEventService{
		db: db, spiceDB: spiceDB, runtime: runtime,
		creditMembers: creditMembers, fileDeleter: deleter,
	}
}

func NewAuditedProgramEventService(
	db *gorm.DB,
	runtime Runtime,
	fileDeleter FileDeleter,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	creditMembers CreditMemberSummaries,
	options ...ProgramEventServiceOption,
) *ProgramEventService {
	if auditWriter == nil {
		panic("program event audit writer is required")
	}
	service := NewProgramEventService(db, runtime, spiceDB, creditMembers, fileDeleter)
	service.auditWriter = auditWriter
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

type ProgramEventSeriesService struct {
	managev1connect.UnimplementedProgramEventSeriesServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	runtime     Runtime
	auditWriter domainaudit.Appender
}

func NewProgramEventSeriesService(db *gorm.DB, runtime Runtime, spiceDB *auth.SpiceDBClient) *ProgramEventSeriesService {
	if db == nil {
		panic("db is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	dependencycheck.MustNotNil(runtime, "Program Event runtime")
	return &ProgramEventSeriesService{db: db, spiceDB: spiceDB, runtime: runtime}
}

func NewAuditedProgramEventSeriesService(db *gorm.DB, runtime Runtime, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *ProgramEventSeriesService {
	if auditWriter == nil {
		panic("program event series audit writer is required")
	}
	service := NewProgramEventSeriesService(db, runtime, spiceDB)
	service.auditWriter = auditWriter
	return service
}

type ProgramEventTypeService struct {
	managev1connect.UnimplementedProgramEventTypeServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewProgramEventTypeService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *ProgramEventTypeService {
	if db == nil {
		panic("db is required")
	}
	dependencycheck.MustNotNil(spiceDB, "spiceDB")
	return &ProgramEventTypeService{db: db, spiceDB: spiceDB}
}

func NewAuditedProgramEventTypeService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *ProgramEventTypeService {
	if auditWriter == nil {
		panic("program event type audit writer is required")
	}
	service := NewProgramEventTypeService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func (s *ProgramEventService) CreateProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.CreateProgramEventRequest],
) (*connect.Response[managev1.ProgramEvent], error) {
	can, err := policyv1.ProgramEvent.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	event, err := s.newProgramEvent(ctx, req.Msg, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := s.createProgramEventWithDB(ctx, tx, event, req.Msg); err != nil {
			return err
		}
		apply, err := policyv1.ProgramEvent.TouchPolicy(event.ID)
		if err != nil {
			return err
		}
		compensate, err := policyv1.ProgramEvent.DeletePolicy(event.ID)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		return nil, err
	}
	proto, err := s.loadProgramEvent(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventService) newProgramEvent(
	ctx context.Context,
	request *managev1.CreateProgramEventRequest,
	acceptLanguage string,
) (*model.ProgramEvent, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return nil, errs.Required("title")
	}
	slug, err := validateProgramEventSlug(request.Slug)
	if err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event", "events", slug); err != nil {
		return nil, err
	}
	if request.StartsAt == nil {
		return nil, errs.Required("starts_at")
	}
	startsAt := request.StartsAt.AsTime()
	endsAt := timestampPtr(request.EndsAt)
	if err := validateProgramEventTimeRange(startsAt, endsAt); err != nil {
		return nil, err
	}
	timezone := strings.TrimSpace(request.Timezone)
	if timezone == "" {
		return nil, errs.Required("timezone")
	}
	locationMode := programEventLocationModeString(request.LocationMode)
	mapPlaceID := request.MapPlaceId
	if !programEventLocationModeUsesMapPlace(locationMode) {
		mapPlaceID = nil
	}
	if err := validateProgramEventLocation(locationMode, mapPlaceID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	sourceLocale := ""
	if strings.TrimSpace(request.SourceLocale) == "" {
		sourceLocale = resolveInitialSourceLocale(ctx, s.db, acceptLanguage)
	} else {
		sourceLocale, err = normalizeRequiredProgramEventLocale("source_locale", request.SourceLocale)
		if err != nil {
			return nil, err
		}
	}
	return &model.ProgramEvent{
		Title:        title,
		Slug:         slug,
		Status:       managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String(),
		SourceLocale: sourceLocale,
		TypeID:       request.TypeId,
		SeriesID:     request.SeriesId,
		SeriesOrder:  request.SeriesOrder,
		StartsAt:     startsAt,
		EndsAt:       endsAt,
		Timezone:     timezone,
		AllDay:       request.AllDay,
		LocationMode: locationMode,
		MapPlaceID:   mapPlaceID,
		TicketURL:    request.TicketUrl,
		StreamURL:    request.StreamUrl,
		ExternalURL:  request.ExternalUrl,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (s *ProgramEventService) createProgramEventWithDB(
	ctx context.Context,
	tx *gorm.DB,
	event *model.ProgramEvent,
	request *managev1.CreateProgramEventRequest,
) error {
	if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event", "events", event.Slug); err != nil {
		return err
	}
	if err := validateProgramEventSeriesRelation(ctx, tx, event.ID, event.SeriesID, event.SeriesOrder); err != nil {
		return err
	}
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      programEventContentProfile,
		SourceLocale: event.SourceLocale,
	})
	if err != nil {
		return normalizeProgramEventContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	event.ContentDocumentID = &contentDocumentID
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(event).Error; err != nil {
		if dberrors.IsUniqueViolation(err) {
			return errs.SlugAlreadyExists("program event", event.Slug)
		}
		return errs.Internal(err)
	}
	if err := initializeProgramEventBlockTranslationSource(
		ctx,
		tx,
		event.ID,
		event.SourceLocale,
		request.Summary,
		event.CreatedAt,
	); err != nil {
		return err
	}
	if request.PosterFileId != nil && strings.TrimSpace(request.GetPosterFileId()) != "" {
		if err := addProgramEventMedia(ctx, tx, s.runtime, event.ID, request.GetPosterFileId(), "poster", nil, nil, true); err != nil {
			return err
		}
	}
	if err := replaceProgramEventArtists(ctx, tx, event.ID, request.Artists); err != nil {
		return err
	}
	if err := replaceProgramEventLabels(ctx, tx, event.ID, request.Labels); err != nil {
		return err
	}
	if err := replaceProgramEventClients(ctx, tx, event.ID, request.Clients); err != nil {
		return err
	}
	if _, err := replaceProgramEventCredits(ctx, tx, event.ID, request.Credits); err != nil {
		return err
	}
	return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewProgramEventCreatedAuditRecord(metadata, event.ID)
	})
}

func (s *ProgramEventService) GetProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.GetProgramEventRequest],
) (*connect.Response[managev1.ProgramEvent], error) {
	proto, err := s.loadAuthorizedProgramEvent(ctx, req.Msg.Id)
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("program event", req.Msg.Id)
		}
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventService) ListProgramEventsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListProgramEventsAdminRequest],
) (*connect.Response[managev1.ListProgramEventsAdminResponse], error) {
	can, err := policyv1.ProgramEvent.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Model(&model.ProgramEvent{})
	query, err = ProgramEventFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query, err = programEventSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	limit, offset := paginationLimitOffset(req.Msg.Pagination, 50)
	var events []model.ProgramEvent
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&events).Error; err != nil {
		return nil, errs.Internal(err)
	}
	eventIDs := collectProgramEventIDs(events)
	posterFileIDs, err := loadProgramEventDefaultPosterFileIDs(ctx, s.db, eventIDs)
	if err != nil {
		return nil, err
	}
	summaries := make([]*managev1.ProgramEventSummary, 0, len(events))
	for i := range events {
		summaries = append(summaries, toProtoProgramEventSummary(&events[i], posterFileIDs[events[i].ID]))
	}
	return connect.NewResponse(&managev1.ListProgramEventsAdminResponse{
		Events: summaries,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+int32(len(events)) < int32(total),
		},
	}), nil
}

func (s *ProgramEventService) DeleteProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.DeleteProgramEventRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Program Event content Block store is not configured")
	}
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		documentID, err := loadProgramEventContentDocumentID(ctx, tx, req.Msg.Id, true)
		if err != nil {
			return err
		}
		if err := requireProgramEventMutationPermissionWithDB(
			ctx, tx, s.spiceDB, req.Msg.Id, policyv1.ProgramEvent.Delete,
		); err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			programEventContentDocumentFence(req.Msg.Id, func(context.Context, *gorm.DB) error { return nil }),
		); err != nil {
			return normalizeProgramEventContentBlockError(err)
		}
		result := tx.Delete(&model.ProgramEvent{}, "id = ?", req.Msg.Id)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected == 0 {
			return errs.NotFound("program event", req.Msg.Id)
		}
		if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "program_event", req.Msg.Id, "media"); err != nil {
			return err
		}
		if err := appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventDeletedAuditRecord(metadata, req.Msg.Id)
		}); err != nil {
			return err
		}
		apply, err := policyv1.ProgramEvent.DeletePolicy(req.Msg.Id)
		if err != nil {
			return err
		}
		compensate, err := policyv1.ProgramEvent.TouchPolicy(req.Msg.Id)
		if err != nil {
			return err
		}
		return write(
			[]policyv1.RelationshipMutation{apply},
			[]policyv1.RelationshipMutation{compensate},
		)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *ProgramEventService) PublishProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.PublishProgramEventRequest],
) (*connect.Response[managev1.ProgramEventLifecycleMutationResponse], error) {
	return s.setProgramEventStatus(ctx, req.Msg.Id, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(), true)
}

func (s *ProgramEventService) ArchiveProgramEvent(
	ctx context.Context,
	req *connect.Request[managev1.ArchiveProgramEventRequest],
) (*connect.Response[managev1.ProgramEventLifecycleMutationResponse], error) {
	return s.setProgramEventStatus(ctx, req.Msg.Id, managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String(), false)
}

func (s *ProgramEventService) setProgramEventStatus(
	ctx context.Context,
	id string,
	status string,
	publish bool,
) (*connect.Response[managev1.ProgramEventLifecycleMutationResponse], error) {
	var current model.ProgramEvent
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "published_at", "updated_at").
			First(&current, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event", id)
			}
			return errs.Internal(err)
		}
		if err := requireActiveProgramEventPrincipal(ctx, tx, "publish", false); err != nil {
			return err
		}
		if err := requireProgramEventPermission(
			ctx, s.spiceDB, current.ID,
			programEventMutationAction(current.Status, policyv1.ProgramEvent.Publish),
		); err != nil {
			return err
		}
		if publish && current.Status == managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() {
			return errs.FailedPrecondition("program event is already published")
		}
		if !publish && current.Status != managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String() {
			return errs.FailedPrecondition("only published program events can be archived")
		}
		updates := structured.Fields{"status": status, "updated_at": now}
		previousStatus := current.Status
		if publish && current.Status != managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String() {
			updates["published_at"] = now
		}
		if err := tx.Model(&current).Updates(updates).Error; err != nil {
			return errs.Internal(err)
		}
		current.Status = status
		current.UpdatedAt = now
		if publishedAt, ok := updates["published_at"].(time.Time); ok {
			current.PublishedAt = &publishedAt
		}
		return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventLifecycleAuditRecord(metadata, current.ID, programEventAuditState(previousStatus), programEventAuditState(status))
		})
	}); err != nil {
		return nil, err
	}
	response := &managev1.ProgramEventLifecycleMutationResponse{
		Id:        current.ID,
		Changed:   true,
		Status:    manageProgramEventStatus(current.Status),
		UpdatedAt: timestamppb.New(current.UpdatedAt),
	}
	if current.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*current.PublishedAt)
	}
	return connect.NewResponse(response), nil
}

func programEventAuditState(status string) sharedtelemetry.AuditState {
	switch status {
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_DRAFT.String():
		return sharedtelemetry.AuditStateDraft
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String():
		return sharedtelemetry.AuditStatePublished
	case managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String():
		return sharedtelemetry.AuditStateArchived
	default:
		return sharedtelemetry.AuditStateNone
	}
}
