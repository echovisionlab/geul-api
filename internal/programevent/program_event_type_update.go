package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// updateProgramEventType writes command to the locked event type and
// returns the changed configuration fields.
func updateProgramEventType(
	ctx context.Context,
	store Store,
	eventType Type,
	command UpdateProgramEventTypeCommand,
	now time.Time,
) ([]string, error) {
	change, err := prepareProgramEventTypeUpdate(command)
	if err != nil {
		return nil, err
	}
	changedFields := typeChangedFields(change, eventType)
	if len(changedFields) > 0 {
		if err := store.UpdateType(ctx, eventType.ID, change, now); err != nil {
			if errors.Is(err, ErrSlugTaken) {
				return nil, domainerr.Duplicate("program event type", "slug", derefString(command.Slug)).WithCause(err)
			}
			return nil, err
		}
	}
	if _, err := updateProgramEventTypeLocale(ctx, store, command, now); err != nil {
		return nil, err
	}
	return changedFields, nil
}

func lockProgramEventTypeForUpdate(ctx context.Context, store Store, id string) (Type, error) {
	eventType, err := store.Type(ctx, id, UpdateLock)
	if errors.Is(err, ErrTypeNotFound) {
		return eventType, domainerr.ResourceNotFound("program event type", id)
	}
	return eventType, err
}

func prepareProgramEventTypeUpdate(command UpdateProgramEventTypeCommand) (TypeChange, error) {
	change := TypeChange{
		SortOrder:         command.SortOrder,
		RequiresPlace:     command.RequiresPlace,
		RequiresStreamURL: command.RequiresStreamURL,
	}
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
		if err != nil {
			return TypeChange{}, err
		}
		change.Slug = &slug
	}
	if command.Status != nil {
		if *command.Status != TypeStatusActive && *command.Status != TypeStatusInactive {
			return TypeChange{}, domainerr.InvalidField("status", "must be active or inactive")
		}
		change.Status = command.Status
	}
	return change, nil
}

func typeChangedFields(change TypeChange, eventType Type) []string {
	changed := make([]string, 0, 5)
	if change.Slug != nil && *change.Slug != eventType.Slug {
		changed = append(changed, "slug")
	}
	if change.Status != nil && *change.Status != eventType.Status {
		changed = append(changed, "status")
	}
	if change.SortOrder != nil && *change.SortOrder != eventType.SortOrder {
		changed = append(changed, "sort_order")
	}
	if change.RequiresPlace != nil && *change.RequiresPlace != eventType.RequiresPlace {
		changed = append(changed, "requires_place")
	}
	if change.RequiresStreamURL != nil && *change.RequiresStreamURL != eventType.RequiresStreamURL {
		changed = append(changed, "requires_stream_url")
	}
	return changed
}

func updateProgramEventTypeLocale(ctx context.Context, store Store, request UpdateProgramEventTypeCommand, now time.Time) (bool, error) {
	if request.Name == nil && request.Description == nil {
		return false, nil
	}
	if strings.TrimSpace(request.Locale) == "" {
		return false, domainerr.Required("locale")
	}
	locale, err := normalizeRequiredProgramEventLocale("locale", request.Locale)
	if err != nil {
		return false, err
	}
	current, exists, err := store.TypeLocale(ctx, request.ID, locale)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, createProgramEventTypeLocale(ctx, store, request, locale, now)
	}
	return applyProgramEventTypeLocaleUpdate(ctx, store, current, request, now)
}

func createProgramEventTypeLocale(
	ctx context.Context,
	store Store,
	request UpdateProgramEventTypeCommand,
	locale string,
	now time.Time,
) error {
	if request.Name == nil || strings.TrimSpace(*request.Name) == "" {
		return domainerr.Required("name")
	}
	return store.InsertTypeLocale(ctx, TypeLocale{
		TypeID:      request.ID,
		Locale:      locale,
		Name:        strings.TrimSpace(*request.Name),
		Description: normalizedStringPtr(request.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

func applyProgramEventTypeLocaleUpdate(
	ctx context.Context,
	store Store,
	current TypeLocale,
	request UpdateProgramEventTypeCommand,
	now time.Time,
) (bool, error) {
	var change TypeLocaleChange
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			return false, domainerr.Required("name")
		}
		if name != current.Name {
			change.Name = &name
		}
	}
	if request.Description != nil {
		description := normalizedStringPtr(request.Description)
		if !sameNullableString(current.Description, description) {
			change.Description = setNullable(description)
		}
	}
	if change.Name == nil && !change.Description.Set {
		return false, nil
	}
	if err := store.UpdateTypeLocale(ctx, current.TypeID, current.Locale, change, now); err != nil {
		return false, err
	}
	return true, nil
}
