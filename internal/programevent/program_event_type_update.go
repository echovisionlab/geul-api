package programevent

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// updateProgramEventType writes command to the locked event type and
// returns the changed configuration fields.
func updateProgramEventType(
	tx *gorm.DB,
	eventType model.ProgramEventType,
	command UpdateProgramEventTypeCommand,
	now time.Time,
) ([]string, error) {
	update, err := prepareProgramEventTypeUpdate(command)
	if err != nil {
		return nil, err
	}
	changedFields := update.changedFields(eventType)
	if len(changedFields) > 0 {
		if err := tx.Model(&eventType).Updates(update.columns(now)).Error; err != nil {
			if dberrors.IsUniqueViolation(err) {
				return nil, domainerr.Duplicate("program event type", "slug", derefString(command.Slug)).WithCause(err)
			}
			return nil, err
		}
	}
	if _, err := updateProgramEventTypeLocale(tx, command, now); err != nil {
		return nil, err
	}
	return changedFields, nil
}

func lockProgramEventTypeForUpdate(tx *gorm.DB, id string) (model.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&eventType, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return eventType, domainerr.ResourceNotFound("program event type", id)
		}
		return eventType, err
	}
	return eventType, nil
}

// programEventTypeUpdate is a validated event type configuration update; nil
// fields are left alone.
type programEventTypeUpdate struct {
	slug              *string
	status            *model.ProgramEventTypeStatus
	sortOrder         *int32
	requiresPlace     *bool
	requiresStreamURL *bool
}

func prepareProgramEventTypeUpdate(command UpdateProgramEventTypeCommand) (programEventTypeUpdate, error) {
	update := programEventTypeUpdate{
		sortOrder:         command.SortOrder,
		requiresPlace:     command.RequiresPlace,
		requiresStreamURL: command.RequiresStreamURL,
	}
	if command.Slug != nil {
		slug, err := validateProgramEventSlug(*command.Slug)
		if err != nil {
			return programEventTypeUpdate{}, err
		}
		update.slug = &slug
	}
	if command.Status != nil {
		if *command.Status != model.ProgramEventTypeStatusActive && *command.Status != model.ProgramEventTypeStatusInactive {
			return programEventTypeUpdate{}, domainerr.InvalidField("status", "must be active or inactive")
		}
		update.status = command.Status
	}
	return update, nil
}

func (u programEventTypeUpdate) changedFields(eventType model.ProgramEventType) []string {
	changed := make([]string, 0, 5)
	if u.slug != nil && *u.slug != eventType.Slug {
		changed = append(changed, "slug")
	}
	if u.status != nil && *u.status != eventType.Status {
		changed = append(changed, "status")
	}
	if u.sortOrder != nil && *u.sortOrder != eventType.SortOrder {
		changed = append(changed, "sort_order")
	}
	if u.requiresPlace != nil && *u.requiresPlace != eventType.RequiresPlace {
		changed = append(changed, "requires_place")
	}
	if u.requiresStreamURL != nil && *u.requiresStreamURL != eventType.RequiresStreamURL {
		changed = append(changed, "requires_stream_url")
	}
	return changed
}

// columns is the program_event_type row update for the fields the request
// sets.
func (u programEventTypeUpdate) columns(updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if u.slug != nil {
		columns["slug"] = *u.slug
	}
	if u.status != nil {
		columns["status"] = *u.status
	}
	if u.sortOrder != nil {
		columns["sort_order"] = *u.sortOrder
	}
	if u.requiresPlace != nil {
		columns["requires_place"] = *u.requiresPlace
	}
	if u.requiresStreamURL != nil {
		columns["requires_stream_url"] = *u.requiresStreamURL
	}
	return columns
}

func updateProgramEventTypeLocale(tx *gorm.DB, request UpdateProgramEventTypeCommand, now time.Time) (bool, error) {
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
	var localeRow model.ProgramEventTypeLocale
	err = tx.First(&localeRow, "type_id = ? AND locale = ?", request.ID, locale).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, createProgramEventTypeLocale(tx, request, locale, now)
	}
	changed, err := applyProgramEventTypeLocaleUpdate(tx, &localeRow, request, now)
	return changed, err
}

func createProgramEventTypeLocale(
	tx *gorm.DB,
	request UpdateProgramEventTypeCommand,
	locale string,
	now time.Time,
) error {
	if request.Name == nil || strings.TrimSpace(*request.Name) == "" {
		return domainerr.Required("name")
	}
	return tx.Create(&model.ProgramEventTypeLocale{
		TypeID:      request.ID,
		Locale:      locale,
		Name:        strings.TrimSpace(*request.Name),
		Description: normalizedStringPtr(request.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}).Error
}

func applyProgramEventTypeLocaleUpdate(
	tx *gorm.DB,
	localeRow *model.ProgramEventTypeLocale,
	request UpdateProgramEventTypeCommand,
	now time.Time,
) (bool, error) {
	updates := structured.Fields{}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			return false, domainerr.Required("name")
		}
		if name != localeRow.Name {
			updates["name"] = name
		}
	}
	if request.Description != nil {
		description := normalizedStringPtr(request.Description)
		if !sameNullableString(localeRow.Description, description) {
			updates["description"] = description
		}
	}
	if len(updates) == 0 {
		return false, nil
	}
	updates["updated_at"] = now
	if err := tx.Model(localeRow).Updates(updates).Error; err != nil {
		return false, err
	}
	return true, nil
}
