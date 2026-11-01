package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateProgramEventTypeCommand creates an active event type with its name
// in one locale. An empty Locale is resolved from the Member's preference,
// then AcceptLanguage, then the site default.
type CreateProgramEventTypeCommand struct {
	Slug              string
	Locale            string
	AcceptLanguage    string
	Name              string
	Description       *string
	SortOrder         int32
	RequiresPlace     bool
	RequiresStreamURL bool
}

// CreateProgramEventType creates the event type and returns it.
func (s *Service) CreateProgramEventType(ctx context.Context, command CreateProgramEventTypeCommand) (TypeView, error) {
	can, err := policyv1.ProgramEvent.CreateType()
	if err != nil {
		return TypeView{}, err
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return TypeView{}, err
	}
	var locale string
	if strings.TrimSpace(command.Locale) == "" {
		locale = s.resolveInitialSourceLocale(ctx, command.AcceptLanguage)
	} else if locale, err = normalizeRequiredProgramEventLocale("locale", command.Locale); err != nil {
		return TypeView{}, err
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return TypeView{}, domainerr.Required("name")
	}
	now := time.Now().UTC()
	eventType := &Type{
		Slug: slug, Status: TypeStatusActive, SortOrder: command.SortOrder,
		RequiresPlace: command.RequiresPlace, RequiresStreamURL: command.RequiresStreamURL,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			if err := store.InsertType(ctx, eventType); err != nil {
				if errors.Is(err, ErrSlugTaken) {
					return "", domainerr.Duplicate("program event type", "slug", slug).WithCause(err)
				}
				return "", err
			}
			return eventType.ID, store.InsertTypeLocale(ctx, TypeLocale{
				TypeID: eventType.ID, Locale: locale, Name: name, Description: command.Description,
				CreatedAt: now, UpdatedAt: now,
			})
		},
		Audit: func(typeID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventTypeCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventTypeCreatedAuditRecord(metadata, typeID)
			})
		},
	}); err != nil {
		return TypeView{}, err
	}
	return s.loadProgramEventType(ctx, eventType.ID)
}

// ListProgramEventTypesQuery is one page of the administrator type list.
type ListProgramEventTypesQuery struct {
	// Limit defaults to 50.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// TypeList is one page of event types.
type TypeList struct {
	Types      []TypeView
	Pagination Pagination
}

// ListProgramEventTypesAdmin returns one page of event types to an
// administrator.
func (s *Service) ListProgramEventTypesAdmin(ctx context.Context, query ListProgramEventTypesQuery) (TypeList, error) {
	can, err := policyv1.ProgramEvent.ListTypes()
	if err != nil {
		return TypeList{}, err
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return TypeList{}, err
	}
	limit, offset := pageLimitOffset(query.Limit, query.Offset)
	var types []Type
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		types, total, err = store.ListTypes(ctx, ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: limit, Offset: offset})
		return err
	}); err != nil {
		return TypeList{}, err
	}
	result := make([]TypeView, 0, len(types))
	for i := range types {
		item, err := s.typeView(ctx, types[i])
		if err != nil {
			return TypeList{}, err
		}
		result = append(result, item)
	}
	return TypeList{Types: result, Pagination: pagination(limit, offset, len(types), total)}, nil
}

// UpdateProgramEventTypeCommand changes an event type's configuration and
// the name or description of one locale; nil fields are left alone. An
// empty Status is rejected.
type UpdateProgramEventTypeCommand struct {
	ID                string
	Slug              *string
	Status            *TypeStatus
	SortOrder         *int32
	RequiresPlace     *bool
	RequiresStreamURL *bool
	Locale            string
	Name              *string
	Description       *string
}

// UpdateProgramEventType changes the locked event type and returns it.
// Only configuration changes are audited.
func (s *Service) UpdateProgramEventType(ctx context.Context, command UpdateProgramEventTypeCommand) (TypeView, error) {
	can, err := policyv1.ProgramEvent.UpdateType()
	if err != nil {
		return TypeView{}, err
	}
	if strings.TrimSpace(command.ID) == "" {
		return TypeView{}, domainerr.Required("id")
	}
	var eventType Type
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[[]string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			eventType, err = lockProgramEventTypeForUpdate(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) ([]string, error) {
			return updateProgramEventType(ctx, store, eventType, command, time.Now().UTC())
		},
		Audit: func(changedFields []string) mutation.Audit {
			if len(changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventTypeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventTypeConfigUpdatedAuditRecord(metadata, eventType.ID, changedFields)
			})
		},
	}); err != nil {
		return TypeView{}, err
	}
	return s.loadProgramEventType(ctx, command.ID)
}

// DeleteProgramEventTypeCommand names the event type to delete.
type DeleteProgramEventTypeCommand struct {
	ID string
}

// DeleteProgramEventType deletes an event type no event uses.
func (s *Service) DeleteProgramEventType(ctx context.Context, command DeleteProgramEventTypeCommand) error {
	can, err := policyv1.ProgramEvent.DeleteType()
	if err != nil {
		return err
	}
	if strings.TrimSpace(command.ID) == "" {
		return domainerr.Required("id")
	}
	var eventType Type
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			eventType, err = lockProgramEventTypeForUpdate(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (string, error) {
			usageCount, err := store.CountTypeEvents(ctx, command.ID)
			if err != nil {
				return "", err
			}
			if usageCount > 0 {
				return "", domainerr.Precondition("program event type is in use")
			}
			if err := store.DeleteType(ctx, eventType.ID); err != nil {
				if errors.Is(err, ErrTypeInUse) {
					return "", domainerr.Precondition("program event type is in use").WithCause(err)
				}
				return "", err
			}
			return eventType.ID, nil
		},
		Audit: func(typeID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventTypeDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventTypeDeletedAuditRecord(metadata, typeID)
			})
		},
	})
	return err
}

// loadProgramEventType reads a type and its locales after a mutation.
func (s *Service) loadProgramEventType(ctx context.Context, id string) (TypeView, error) {
	var eventType Type
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		eventType, err = store.Type(ctx, id, NoLock)
		return err
	}); err != nil {
		if errors.Is(err, ErrTypeNotFound) {
			return TypeView{}, domainerr.ResourceNotFound("program event type", id)
		}
		return TypeView{}, err
	}
	return s.typeView(ctx, eventType)
}

func (s *Service) typeView(ctx context.Context, eventType Type) (TypeView, error) {
	view := TypeView{Type: eventType}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		view.Locales, err = store.TypeLocales(ctx, eventType.ID)
		return err
	})
	return view, err
}
