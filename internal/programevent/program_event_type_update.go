package programevent

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *ProgramEventTypeService) updateProgramEventType(
	ctx context.Context,
	request *managev1.UpdateProgramEventTypeRequest,
	can policyv1.Can,
	now time.Time,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		eventType, err := lockProgramEventTypeForUpdate(tx, request.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		updates, err := prepareProgramEventTypeUpdates(request, now)
		if err != nil {
			return err
		}
		changedFields := programEventTypeAuditChangedFields(eventType, updates)
		if len(changedFields) > 0 {
			updates["updated_at"] = now
			if err := tx.Model(&eventType).Updates(updates).Error; err != nil {
				if dberrors.IsUniqueViolation(err) {
					return errs.SlugAlreadyExists("program event type", request.GetSlug())
				}
				return errs.Internal(err)
			}
		}
		_, err = updateProgramEventTypeLocale(tx, request, now)
		if err != nil {
			return err
		}
		if len(changedFields) == 0 {
			return nil
		}
		return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventTypeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventTypeConfigUpdatedAuditRecord(metadata, eventType.ID, changedFields)
		})
	})
}

func lockProgramEventTypeForUpdate(tx *gorm.DB, id string) (model.ProgramEventType, error) {
	var eventType model.ProgramEventType
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&eventType, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return eventType, errs.NotFound("program event type", id)
		}
		return eventType, errs.Internal(err)
	}
	return eventType, nil
}

func prepareProgramEventTypeUpdates(request *managev1.UpdateProgramEventTypeRequest, now time.Time) (structured.Fields, error) {
	updates := structured.Fields{}
	if request.Slug != nil {
		slug, err := validateProgramEventSlug(request.GetSlug())
		if err != nil {
			return nil, err
		}
		updates["slug"] = slug
	}
	if request.Status != nil {
		status, err := validateProgramEventTypeStatus(request.GetStatus())
		if err != nil {
			return nil, err
		}
		updates["status"] = status
	}
	if request.SortOrder != nil {
		updates["sort_order"] = request.GetSortOrder()
	}
	if request.RequiresPlace != nil {
		updates["requires_place"] = request.GetRequiresPlace()
	}
	if request.RequiresStreamUrl != nil {
		updates["requires_stream_url"] = request.GetRequiresStreamUrl()
	}
	return updates, nil
}

func programEventTypeAuditChangedFields(eventType model.ProgramEventType, updates structured.Fields) []string {
	changed := make([]string, 0, len(updates))
	if value, ok := updates["slug"].(string); ok && value != eventType.Slug {
		changed = append(changed, "slug")
	}
	if value, ok := updates["status"].(string); ok && value != eventType.Status {
		changed = append(changed, "status")
	}
	if value, ok := updates["sort_order"].(int32); ok && value != eventType.SortOrder {
		changed = append(changed, "sort_order")
	}
	if value, ok := updates["requires_place"].(bool); ok && value != eventType.RequiresPlace {
		changed = append(changed, "requires_place")
	}
	if value, ok := updates["requires_stream_url"].(bool); ok && value != eventType.RequiresStreamURL {
		changed = append(changed, "requires_stream_url")
	}
	return changed
}

func validateProgramEventTypeStatus(status managev1.ProgramEventTypeStatus) (string, error) {
	switch status {
	case managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE,
		managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE:
		return status.String(), nil
	default:
		return "", errs.InvalidArgument("status", "must be active or inactive")
	}
}

func updateProgramEventTypeLocale(tx *gorm.DB, request *managev1.UpdateProgramEventTypeRequest, now time.Time) (bool, error) {
	if request.Name == nil && request.Description == nil {
		return false, nil
	}
	if strings.TrimSpace(request.Locale) == "" {
		return false, errs.Required("locale")
	}
	locale, err := normalizeRequiredProgramEventLocale("locale", request.Locale)
	if err != nil {
		return false, err
	}
	var localeRow model.ProgramEventTypeLocale
	err = tx.First(&localeRow, "type_id = ? AND locale = ?", request.Id, locale).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return false, errs.Internal(err)
	}
	if err == gorm.ErrRecordNotFound {
		return true, createProgramEventTypeLocale(tx, request, locale, now)
	}
	changed, err := applyProgramEventTypeLocaleUpdate(tx, &localeRow, request, now)
	return changed, err
}

func createProgramEventTypeLocale(
	tx *gorm.DB,
	request *managev1.UpdateProgramEventTypeRequest,
	locale string,
	now time.Time,
) error {
	name := strings.TrimSpace(request.GetName())
	if request.Name == nil || name == "" {
		return errs.Required("name")
	}
	return tx.Create(&model.ProgramEventTypeLocale{
		TypeID:      request.Id,
		Locale:      locale,
		Name:        name,
		Description: normalizedStringPtr(request.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}).Error
}

func applyProgramEventTypeLocaleUpdate(
	tx *gorm.DB,
	localeRow *model.ProgramEventTypeLocale,
	request *managev1.UpdateProgramEventTypeRequest,
	now time.Time,
) (bool, error) {
	updates := structured.Fields{}
	if request.Name != nil {
		name := strings.TrimSpace(request.GetName())
		if name == "" {
			return false, errs.Required("name")
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
