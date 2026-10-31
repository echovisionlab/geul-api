package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
func (s *Service) CreateProgramEventType(ctx context.Context, command CreateProgramEventTypeCommand) (*managev1.ProgramEventType, error) {
	can, err := policyv1.ProgramEvent.CreateType()
	if err != nil {
		return nil, err
	}
	slug, err := validateProgramEventSlug(command.Slug)
	if err != nil {
		return nil, err
	}
	var locale string
	if strings.TrimSpace(command.Locale) == "" {
		locale = resolveInitialSourceLocale(ctx, s.db, command.AcceptLanguage)
	} else if locale, err = normalizeRequiredProgramEventLocale("locale", command.Locale); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return nil, domainerr.Required("name")
	}
	now := time.Now().UTC()
	eventType := &model.ProgramEventType{
		Slug: slug, Status: model.ProgramEventTypeStatusActive, SortOrder: command.SortOrder,
		RequiresPlace: command.RequiresPlace, RequiresStreamURL: command.RequiresStreamURL,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (string, error) {
			if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(eventType).Error; err != nil {
				if dberrors.IsUniqueViolation(err) {
					return "", domainerr.Duplicate("program event type", "slug", slug).WithCause(err)
				}
				return "", err
			}
			return eventType.ID, tx.Create(&model.ProgramEventTypeLocale{
				TypeID: eventType.ID, Locale: locale, Name: name, Description: command.Description,
				CreatedAt: now, UpdatedAt: now,
			}).Error
		},
		Audit: func(typeID string) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditProgramEventTypeCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewProgramEventTypeCreatedAuditRecord(metadata, typeID)
			})
		},
	}); err != nil {
		return nil, err
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
	Types      []*managev1.ProgramEventType
	Pagination *commonv1.PaginationResponse
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
	rows := s.db.WithContext(ctx).Model(&model.ProgramEventType{})
	rows, err = ProgramEventTypeFilterConfig.ApplyFilters(rows, query.Filters)
	if err != nil {
		return TypeList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return TypeList{}, err
	}
	rows, err = programEventTypeSortConfig.ApplySort(rows, query.Sorts)
	if err != nil {
		return TypeList{}, err
	}
	limit, offset := pageLimitOffset(query.Limit, query.Offset)
	var types []model.ProgramEventType
	if err := rows.Limit(int(limit)).Offset(int(offset)).Find(&types).Error; err != nil {
		return TypeList{}, err
	}
	result := make([]*managev1.ProgramEventType, 0, len(types))
	for i := range types {
		item, err := s.toProtoProgramEventType(ctx, &types[i])
		if err != nil {
			return TypeList{}, err
		}
		result = append(result, item)
	}
	return TypeList{Types: result, Pagination: &commonv1.PaginationResponse{
		Total: int32(total), Limit: limit, Offset: offset,
		HasMore: int64(offset)+int64(len(types)) < total,
	}}, nil
}

// UpdateProgramEventTypeCommand changes an event type's configuration and
// the name or description of one locale; nil fields are left alone. An
// empty Status is rejected.
type UpdateProgramEventTypeCommand struct {
	ID                string
	Slug              *string
	Status            *model.ProgramEventTypeStatus
	SortOrder         *int32
	RequiresPlace     *bool
	RequiresStreamURL *bool
	Locale            string
	Name              *string
	Description       *string
}

// UpdateProgramEventType changes the locked event type and returns it.
// Only configuration changes are audited.
func (s *Service) UpdateProgramEventType(ctx context.Context, command UpdateProgramEventTypeCommand) (*managev1.ProgramEventType, error) {
	can, err := policyv1.ProgramEvent.UpdateType()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(command.ID) == "" {
		return nil, domainerr.Required("id")
	}
	var eventType model.ProgramEventType
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[[]string]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			eventType, err = lockProgramEventTypeForUpdate(tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) ([]string, error) {
			return updateProgramEventType(tx, eventType, command, time.Now().UTC())
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
		return nil, err
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
	var eventType model.ProgramEventType
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&eventType, "id = ?", command.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("program event type", command.ID)
				}
				return err
			}
			return nil
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (string, error) {
			var usageCount int64
			if err := tx.Model(&model.ProgramEvent{}).Where("type_id = ?", command.ID).Count(&usageCount).Error; err != nil {
				return "", err
			}
			if usageCount > 0 {
				return "", domainerr.Precondition("program event type is in use")
			}
			if err := tx.Delete(&eventType).Error; err != nil {
				if dberrors.IsForeignKeyViolation(err) {
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
