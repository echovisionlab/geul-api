package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

const EntityType = "program_event"

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
	LocationMode   LocationMode
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
func (s *Service) CreateProgramEvent(ctx context.Context, command CreateProgramEventCommand) (EventView, error) {
	can, err := policyv1.ProgramEvent.Create()
	if err != nil {
		return EventView{}, err
	}
	event, err := s.newProgramEvent(ctx, command)
	if err != nil {
		return EventView{}, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			return event.ID, s.createProgramEvent(ctx, store, event, command)
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
		return EventView{}, err
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

// checkRoute requires, in a snapshot of its own, that no Page uses the
// slug's route.
func (s *Service) checkRoute(ctx context.Context, kind RouteKind, slug string) error {
	return s.units.Read(ctx, func(store Store) error {
		return store.CheckRoute(ctx, kind, slug)
	})
}

func (s *Service) newProgramEvent(ctx context.Context, command CreateProgramEventCommand) (*Event, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return nil, err
	}
	if err := s.checkRoute(ctx, EventRoute, slug); err != nil {
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
	return &Event{
		Title: title, Slug: slug, Status: StatusDraft, SourceLocale: sourceLocale,
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

func (s *Service) createProgramEvent(
	ctx context.Context,
	store Store,
	event *Event,
	command CreateProgramEventCommand,
) error {
	if err := store.LockRoute(ctx, EventRoute, event.Slug); err != nil {
		return err
	}
	if err := validateProgramEventSeriesRelation(ctx, store, event.ID, event.SeriesID, event.SeriesOrder); err != nil {
		return err
	}
	document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{
		Profile:      programEventContentProfile,
		SourceLocale: event.SourceLocale,
	})
	if err != nil {
		return normalizeProgramEventContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	event.ContentDocumentID = &contentDocumentID
	if err := store.InsertEvent(ctx, event); err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return domainerr.Duplicate("program event", "slug", event.Slug).WithCause(err)
		}
		return err
	}
	if err := initializeProgramEventBlockTranslationSource(
		ctx, store, event.ID, event.SourceLocale, command.Summary, event.CreatedAt,
	); err != nil {
		return err
	}
	if command.PosterFileID != nil && strings.TrimSpace(*command.PosterFileID) != "" {
		if err := addProgramEventMedia(ctx, store, event.ID, *command.PosterFileID, "poster", nil, nil, true); err != nil {
			return err
		}
	}
	for _, relation := range []struct {
		kind  RelationKind
		field string
		rows  []Relation
	}{
		{ArtistRelation, "artist_id", command.Artists},
		{LabelRelation, "label_id", command.Labels},
		{ClientRelation, "client_id", command.Clients},
	} {
		if err := replaceProgramEventRelations(ctx, store, relation.kind, relation.field, event.ID, relation.rows); err != nil {
			return err
		}
	}
	_, err = replaceProgramEventCredits(ctx, store, event.ID, command.Credits)
	return err
}

// GetProgramEventQuery names the event to read.
type GetProgramEventQuery struct {
	ID string
}

// GetProgramEvent returns the administrator view of an event the caller may
// view; an event the caller may not view answers not found.
func (s *Service) GetProgramEvent(ctx context.Context, query GetProgramEventQuery) (EventView, error) {
	event, err := s.loadAuthorizedProgramEvent(ctx, query.ID)
	if rejected(err, domainerr.PermissionDenied) {
		return EventView{}, domainerr.ResourceNotFound("program event", query.ID).WithCause(err)
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
	Events     []EventSummary
	Pagination Pagination
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
	limit, offset := pageLimitOffset(query.Limit, query.Offset)
	var events []Event
	var total int64
	var posterFileIDs map[string]string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		events, total, err = store.ListEvents(ctx, ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: limit, Offset: offset})
		if err != nil {
			return err
		}
		posterFileIDs, err = store.PosterFileIDs(ctx, collectProgramEventIDs(events))
		return err
	}); err != nil {
		return EventList{}, err
	}
	summaries := make([]EventSummary, 0, len(events))
	for i := range events {
		summary := EventSummary{Event: events[i]}
		if fileID, ok := posterFileIDs[events[i].ID]; ok {
			summary.PosterFileID = &fileID
		}
		summaries = append(summaries, summary)
	}
	return EventList{Events: summaries, Pagination: pagination(limit, offset, len(events), total)}, nil
}

// DeleteProgramEventCommand names the event to delete.
type DeleteProgramEventCommand struct {
	ID string
}

// DeleteProgramEvent deletes the event, its content document and its
// authorization policy.
func (s *Service) DeleteProgramEvent(ctx context.Context, command DeleteProgramEventCommand) error {
	var documentID uuid.UUID
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			documentID, err = loadProgramEventContentDocumentID(ctx, store, command.ID, UpdateLock)
			return err
		},
		Access: mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return requireLockedProgramEventAction(ctx, store, lockActive, s.spiceDB, command.ID, policyv1.ProgramEvent.Delete)
		}),
		Change: func(ctx context.Context, store Store) (string, error) {
			if err := store.DeleteContentDocument(ctx, documentID, lockedContentFence(command.ID, documentID)); err != nil {
				return "", normalizeProgramEventContentBlockError(err)
			}
			deleted, err := store.DeleteEvent(ctx, command.ID)
			if err != nil {
				return "", err
			}
			if !deleted {
				return "", domainerr.ResourceNotFound("program event", command.ID)
			}
			return command.ID, store.ReleaseEventMedia(ctx, command.ID)
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
	Status      Status
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
	return s.setProgramEventStatus(ctx, command.ID, StatusPublished, true)
}

// ArchiveProgramEventCommand names the event to archive.
type ArchiveProgramEventCommand struct {
	ID string
}

// ArchiveProgramEvent moves a published event to archived.
func (s *Service) ArchiveProgramEvent(ctx context.Context, command ArchiveProgramEventCommand) (LifecycleChange, error) {
	return s.setProgramEventStatus(ctx, command.ID, StatusArchived, false)
}

func (s *Service) setProgramEventStatus(
	ctx context.Context,
	id string,
	status Status,
	publish bool,
) (LifecycleChange, error) {
	var current Lifecycle
	var previous Status
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			current, err = store.LockLifecycle(ctx, id)
			if err != nil {
				return notFoundAs(err, domainerr.ResourceNotFound("program event", id))
			}
			previous = current.Status
			return nil
		},
		Access: mutation.Fenced(func(ctx context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
			return authorizeLockedProgramEvent(ctx, lockActive, s.spiceDB, current.ID, current.Status, "publish", policyv1.ProgramEvent.Publish)
		}),
		Change: func(ctx context.Context, store Store) (string, error) {
			if publish && current.Status == StatusPublished {
				return "", domainerr.Precondition("program event is already published")
			}
			if !publish && current.Status != StatusPublished {
				return "", domainerr.Precondition("only published program events can be archived")
			}
			now := dbtime.Now()
			var publishedAt *time.Time
			if publish && current.Status != StatusArchived {
				publishedAt = &now
			}
			stored, err := store.SetLifecycle(ctx, current.ID, status, publishedAt, now)
			if err != nil {
				return "", err
			}
			current = stored
			return current.ID, nil
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
