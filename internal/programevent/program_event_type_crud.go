package programevent

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *ProgramEventTypeService) CreateProgramEventType(
	ctx context.Context,
	req *connect.Request[managev1.CreateProgramEventTypeRequest],
) (*connect.Response[managev1.ProgramEventType], error) {
	can, err := policyv1.ProgramEvent.CreateType()
	if err != nil {
		return nil, errs.Internal(err)
	}
	slug, err := validateProgramEventSlug(req.Msg.Slug)
	if err != nil {
		return nil, err
	}
	locale := ""
	if strings.TrimSpace(req.Msg.Locale) == "" {
		locale = resolveInitialSourceLocale(ctx, s.db, req.Header().Get("Accept-Language"))
	} else {
		locale, err = normalizeRequiredProgramEventLocale("locale", req.Msg.Locale)
		if err != nil {
			return nil, err
		}
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}

	now := time.Now().UTC()
	eventType := &model.ProgramEventType{
		Slug:              slug,
		Status:            managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String(),
		SortOrder:         req.Msg.GetSortOrder(),
		RequiresPlace:     req.Msg.GetRequiresPlace(),
		RequiresStreamURL: req.Msg.GetRequiresStreamUrl(),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(eventType).Error; err != nil {
			if dberrors.IsUniqueViolation(err) {
				return errs.SlugAlreadyExists("program event type", slug)
			}
			return errs.Internal(err)
		}
		localeRow := &model.ProgramEventTypeLocale{
			TypeID:      eventType.ID,
			Locale:      locale,
			Name:        name,
			Description: req.Msg.Description,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Create(localeRow).Error; err != nil {
			return errs.Internal(err)
		}
		return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventTypeCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventTypeCreatedAuditRecord(metadata, eventType.ID)
		})
	}); err != nil {
		return nil, err
	}

	proto, err := s.loadProgramEventType(ctx, eventType.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

func (s *ProgramEventTypeService) ListProgramEventTypesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListProgramEventTypesAdminRequest],
) (*connect.Response[managev1.ListProgramEventTypesAdminResponse], error) {
	can, err := policyv1.ProgramEvent.ListTypes()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&model.ProgramEventType{})
	query, err = ProgramEventTypeFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	query, err = programEventTypeSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	limit, offset := paginationLimitOffset(req.Msg.Pagination, 50)

	var rows []model.ProgramEventType
	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	result := make([]*managev1.ProgramEventType, 0, len(rows))
	for i := range rows {
		item, err := s.toProtoProgramEventType(ctx, &rows[i])
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}

	return connect.NewResponse(&managev1.ListProgramEventTypesAdminResponse{
		Types: result,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+int32(len(rows)) < int32(total),
		},
	}), nil
}

func (s *ProgramEventTypeService) UpdateProgramEventType(
	ctx context.Context,
	req *connect.Request[managev1.UpdateProgramEventTypeRequest],
) (*connect.Response[managev1.ProgramEventType], error) {
	can, err := policyv1.ProgramEvent.UpdateType()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if strings.TrimSpace(req.Msg.Id) == "" {
		return nil, errs.Required("id")
	}

	if err := s.updateProgramEventType(ctx, req.Msg, can, time.Now().UTC()); err != nil {
		return nil, err
	}

	result, err := s.loadProgramEventType(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(result), nil
}

func (s *ProgramEventTypeService) DeleteProgramEventType(
	ctx context.Context,
	req *connect.Request[managev1.DeleteProgramEventTypeRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.ProgramEvent.DeleteType()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if strings.TrimSpace(req.Msg.Id) == "" {
		return nil, errs.Required("id")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var eventType model.ProgramEventType
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&eventType, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("program event type", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var usageCount int64
		if err := tx.Model(&model.ProgramEvent{}).Where("type_id = ?", req.Msg.Id).Count(&usageCount).Error; err != nil {
			return errs.Internal(err)
		}
		if usageCount > 0 {
			return errs.FailedPrecondition("program event type is in use")
		}
		if err := tx.Delete(&eventType).Error; err != nil {
			if dberrors.IsForeignKeyViolation(err) {
				return errs.FailedPrecondition("program event type is in use")
			}
			return errs.Internal(err)
		}
		return appendOptionalProgramEventAudit(ctx, tx, s.auditWriter, sharedtelemetry.AuditProgramEventTypeDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewProgramEventTypeDeletedAuditRecord(metadata, eventType.ID)
		})
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}
