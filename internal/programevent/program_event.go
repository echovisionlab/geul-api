package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
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
				string(model.ProgramEventStatusDraft),
				string(model.ProgramEventStatusPublished),
				string(model.ProgramEventStatusArchived),
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
				string(model.ProgramEventStatusDraft),
				string(model.ProgramEventStatusPublished),
			},
		},
	},
}

var ProgramEventTypeFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"id": {
			Column:     "id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.ProgramEventTypeStatusActive),
				string(model.ProgramEventTypeStatusInactive),
			},
		},
	},
}

const (
	programEventSourceTitleSQL       = "program_event.title"
	programEventSeriesSourceTitleSQL = "program_event_series.title"
)

// Relation is an artist, label or client of a Program Event: the related
// row's ID, its optional role, and its position.
type Relation struct {
	ID        string
	Role      *string
	SortOrder int32
}

// Credit is one credit line of a Program Event. Exactly one of ArtistID,
// MemberID and DisplayName names the credited party; an empty ID creates
// the credit.
type Credit struct {
	ID          string
	ArtistID    *string
	MemberID    *string
	DisplayName *string
	CreditRole  *string
	Description *string
	SortOrder   int32
}

// CreateProgramEventCommand creates a draft event with an empty content
// document. An empty SourceLocale is resolved from the Member's preference,
// then AcceptLanguage, then the site default.
type CreateProgramEventCommand struct {
	Title          string
	Slug           string
	SourceLocale   string
	AcceptLanguage string
	TypeID         string
	StartsAt       *time.Time
	EndsAt         *time.Time
	Timezone       string
	AllDay         bool
	LocationMode   model.ProgramEventLocationMode
	MapPlaceID     *string
	Summary        *string
	SeriesID       *string
	SeriesOrder    *int32
	PosterFileID   *string
	TicketURL      *string
	StreamURL      *string
	ExternalURL    *string
	Artists        []Relation
	Labels         []Relation
	Clients        []Relation
	Credits        []Credit
}

// CreateProgramEvent creates the event and its authorization policy and
// returns the administrator view of it.
func (s *Service) CreateProgramEvent(ctx context.Context, command CreateProgramEventCommand) (*managev1.ProgramEvent, error) {
	can, err := policyv1.ProgramEvent.Create()
	if err != nil {
		return nil, err
	}
	event, err := s.newProgramEvent(ctx, command)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			return event.ID, s.createProgramEventWithDB(ctx, tx, event, command)
		},
		Audit: func(eventID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventCreatedAuditRecord(metadata, eventID)
			})
		},
		Relationships: func(eventID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyRelationships(policyv1.ProgramEvent.TouchPolicy, policyv1.ProgramEvent.DeletePolicy, eventID)
		},
	}); err != nil {
		return nil, err
	}
	return s.loadProgramEvent(ctx, event.ID)
}

// policyRelationships is the relationship batch apply(id) and its exact
// inverse compensate(id).
func policyRelationships(
	apply, compensate func(string) (policyv1.RelationshipMutation, error),
	id string,
) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
	applied, err := apply(id)
	if err != nil {
		return nil, nil, err
	}
	compensated, err := compensate(id)
	if err != nil {
		return nil, nil, err
	}
	return []policyv1.RelationshipMutation{applied}, []policyv1.RelationshipMutation{compensated}, nil
}

func (s *Service) newProgramEvent(ctx context.Context, command CreateProgramEventCommand) (*model.ProgramEvent, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "program event", "events", slug); err != nil {
		return nil, err
	}
	if command.StartsAt == nil {
		return nil, domainerr.Required("starts_at")
	}
	startsAt := *command.StartsAt
	if err := validateProgramEventTimeRange(startsAt, command.EndsAt); err != nil {
		return nil, err
	}
	timezone := strings.TrimSpace(command.Timezone)
	if timezone == "" {
		return nil, domainerr.Required("timezone")
	}
	mapPlaceID := command.MapPlaceID
	if !command.LocationMode.UsesMapPlace() {
		mapPlaceID = nil
	}
	if err := validateProgramEventLocation(command.LocationMode, mapPlaceID); err != nil {
		return nil, err
	}
	sourceLocale, err := s.initialSourceLocale(ctx, command)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &model.ProgramEvent{
		Title: title, Slug: slug, Status: model.ProgramEventStatusDraft, SourceLocale: sourceLocale,
		TypeID: command.TypeID, SeriesID: command.SeriesID, SeriesOrder: command.SeriesOrder,
		StartsAt: startsAt, EndsAt: command.EndsAt, Timezone: timezone, AllDay: command.AllDay,
		LocationMode: command.LocationMode, MapPlaceID: mapPlaceID,
		TicketURL: command.TicketURL, StreamURL: command.StreamURL, ExternalURL: command.ExternalURL,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Service) initialSourceLocale(ctx context.Context, command CreateProgramEventCommand) (string, error) {
	if strings.TrimSpace(command.SourceLocale) == "" {
		return s.resolveInitialSourceLocale(ctx, command.AcceptLanguage), nil
	}
	return normalizeRequiredProgramEventLocale("source_locale", command.SourceLocale)
}

func (s *Service) createProgramEventWithDB(
	ctx context.Context,
	tx *gorm.DB,
	event *model.ProgramEvent,
	command CreateProgramEventCommand,
) error {
	if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "program event", "events", event.Slug); err != nil {
		return err
	}
	if err := validateProgramEventSeriesRelation(tx, event.ID, event.SeriesID, event.SeriesOrder); err != nil {
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
			return domainerr.Duplicate("program event", "slug", event.Slug).WithCause(err)
		}
		return err
	}
	if err := initializeProgramEventBlockTranslationSource(
		ctx, tx, event.ID, event.SourceLocale, command.Summary, event.CreatedAt,
	); err != nil {
		return err
	}
	if command.PosterFileID != nil && strings.TrimSpace(*command.PosterFileID) != "" {
		if err := addProgramEventMedia(ctx, tx, s.media, event.ID, *command.PosterFileID, "poster", nil, nil, true); err != nil {
			return err
		}
	}
	if err := replaceProgramEventArtists(ctx, tx, event.ID, command.Artists); err != nil {
		return err
	}
	if err := replaceProgramEventLabels(ctx, tx, event.ID, command.Labels); err != nil {
		return err
	}
	if err := replaceProgramEventClients(ctx, tx, event.ID, command.Clients); err != nil {
		return err
	}
	_, err = replaceProgramEventCredits(ctx, tx, event.ID, command.Credits)
	return err
}

// GetProgramEventQuery names the event to read.
type GetProgramEventQuery struct {
	ID string
}

// GetProgramEvent returns the administrator view of an event the caller may
// view; an event the caller may not view answers not found.
func (s *Service) GetProgramEvent(ctx context.Context, query GetProgramEventQuery) (*managev1.ProgramEvent, error) {
	event, err := s.loadAuthorizedProgramEvent(ctx, query.ID)
	if rejected(err, domainerr.PermissionDenied) {
		return nil, domainerr.ResourceNotFound("program event", query.ID).WithCause(err)
	}
	return event, err
}

// ListProgramEventsQuery is one page of the administrator event list.
type ListProgramEventsQuery struct {
	// Limit defaults to 50.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// EventList is one page of event summaries.
type EventList struct {
	Events     []*managev1.ProgramEventSummary
	Pagination *commonv1.PaginationResponse
}

// ListProgramEventsAdmin returns one page of event summaries to an
// administrator.
func (s *Service) ListProgramEventsAdmin(ctx context.Context, query ListProgramEventsQuery) (EventList, error) {
	can, err := policyv1.ProgramEvent.List()
	if err != nil {
		return EventList{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return EventList{}, err
	}
	rows := s.db.WithContext(ctx).Model(&model.ProgramEvent{})
	rows, err = ProgramEventFilterConfig.ApplyFilters(rows, query.Filters)
	if err != nil {
		return EventList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return EventList{}, err
	}
	rows, err = programEventSortConfig.ApplySort(rows, query.Sorts)
	if err != nil {
		return EventList{}, err
	}
	limit, offset := pageLimitOffset(query.Limit, query.Offset)
	var events []model.ProgramEvent
	if err := rows.Order("program_event.id ASC").Limit(int(limit)).Offset(int(offset)).Find(&events).Error; err != nil {
		return EventList{}, err
	}
	posterFileIDs, err := loadProgramEventDefaultPosterFileIDs(ctx, s.db, collectProgramEventIDs(events))
	if err != nil {
		return EventList{}, err
	}
	summaries := make([]*managev1.ProgramEventSummary, 0, len(events))
	for i := range events {
		summaries = append(summaries, toProtoProgramEventSummary(&events[i], posterFileIDs[events[i].ID]))
	}
	return EventList{Events: summaries, Pagination: &commonv1.PaginationResponse{
		Total: int32(total), Limit: limit, Offset: offset,
		HasMore: offset+int32(len(events)) < int32(total),
	}}, nil
}

// DeleteProgramEventCommand names the event to delete.
type DeleteProgramEventCommand struct {
	ID string
}

// DeleteProgramEvent deletes the event, its content document and its
// authorization policy.
func (s *Service) DeleteProgramEvent(ctx context.Context, command DeleteProgramEventCommand) error {
	var documentID uuid.UUID
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			documentID, err = loadProgramEventContentDocumentID(ctx, tx, command.ID, true)
			return err
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return requireLockedProgramEventAction(ctx, tx, s.spiceDB, command.ID, policyv1.ProgramEvent.Delete)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			if err := s.contentBlocks.DeleteDocument(ctx, tx, documentID, lockedContentFence(command.ID, documentID)); err != nil {
				return "", normalizeProgramEventContentBlockError(err)
			}
			result := tx.Delete(&model.ProgramEvent{}, "id = ?", command.ID)
			if result.Error != nil {
				return "", result.Error
			}
			if result.RowsAffected == 0 {
				return "", domainerr.ResourceNotFound("program event", command.ID)
			}
			return command.ID, s.media.ReleasePublicAssetBindings(ctx, tx, "program_event", command.ID, "media")
		},
		Audit: func(eventID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventDeletedAuditRecord(metadata, eventID)
			})
		},
		Relationships: func(eventID string) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return policyRelationships(policyv1.ProgramEvent.DeletePolicy, policyv1.ProgramEvent.TouchPolicy, eventID)
		},
	})
	return err
}

// LifecycleChange is the event lifecycle after a publish or archive.
type LifecycleChange struct {
	ID          string
	Changed     bool
	Status      model.ProgramEventStatus
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

// PublishProgramEventCommand names the event to publish.
type PublishProgramEventCommand struct {
	ID string
}

// PublishProgramEvent publishes a draft event, or restores an archived event
// with its original publication date.
func (s *Service) PublishProgramEvent(ctx context.Context, command PublishProgramEventCommand) (LifecycleChange, error) {
	return s.setProgramEventStatus(ctx, command.ID, model.ProgramEventStatusPublished, true)
}

// ArchiveProgramEventCommand names the event to archive.
type ArchiveProgramEventCommand struct {
	ID string
}

// ArchiveProgramEvent moves a published event to archived.
func (s *Service) ArchiveProgramEvent(ctx context.Context, command ArchiveProgramEventCommand) (LifecycleChange, error) {
	return s.setProgramEventStatus(ctx, command.ID, model.ProgramEventStatusArchived, false)
}

func (s *Service) setProgramEventStatus(
	ctx context.Context,
	id string,
	status model.ProgramEventStatus,
	publish bool,
) (LifecycleChange, error) {
	var current model.ProgramEvent
	var previous model.ProgramEventStatus
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Select("id", "status", "published_at", "updated_at").
				First(&current, "id = ?", id).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("program event", id)
				}
				return err
			}
			previous = current.Status
			return nil
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return authorizeLockedProgramEvent(ctx, tx, s.spiceDB, current.ID, current.Status, "publish", policyv1.ProgramEvent.Publish)
		}),
		Change: func(_ context.Context, tx *gorm.DB) (string, error) {
			if publish && current.Status == model.ProgramEventStatusPublished {
				return "", domainerr.Precondition("program event is already published")
			}
			if !publish && current.Status != model.ProgramEventStatusPublished {
				return "", domainerr.Precondition("only published program events can be archived")
			}
			now := dbtime.Now()
			updates := structured.Fields{"status": status, "updated_at": now}
			if publish && current.Status != model.ProgramEventStatusArchived {
				updates["published_at"] = now
			}
			return current.ID, tx.Model(&current).Clauses(clause.Returning{Columns: []clause.Column{
				{Name: "status"}, {Name: "published_at"}, {Name: "updated_at"},
			}}).Updates(updates).Error
		},
		Audit: func(eventID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventLifecycleAuditRecord(metadata, eventID, programEventAuditState(previous), programEventAuditState(status))
			})
		},
	})
	if err != nil {
		return LifecycleChange{}, err
	}
	return LifecycleChange{
		ID: current.ID, Changed: true, Status: current.Status,
		PublishedAt: current.PublishedAt, UpdatedAt: current.UpdatedAt,
	}, nil
}
