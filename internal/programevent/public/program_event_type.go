package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ListProgramEventTypesQuery is one page of active event types, named in
// the locale AcceptLanguage selects.
type ListProgramEventTypesQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	AcceptLanguage string
}

// TypeList is one page of active event types.
type TypeList struct {
	Types      []*openv1.ProgramEventType
	Pagination *commonv1.PaginationResponse
}

// ListProgramEventTypes returns one page of active event types in their
// sort order.
func (s *Service) ListProgramEventTypes(ctx context.Context, query ListProgramEventTypesQuery) (TypeList, error) {
	rows := s.db.WithContext(ctx).
		Model(&model.ProgramEventType{}).
		Where("status = ?", model.ProgramEventTypeStatusActive)
	rows, err := applySimpleProgramEventTypeFilters(rows, query.Filters)
	if err != nil {
		return TypeList{}, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return TypeList{}, err
	}
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var types []model.ProgramEventType
	if err := pagination.Apply(rows.Order("sort_order ASC, slug ASC")).Find(&types).Error; err != nil {
		return TypeList{}, err
	}
	typeIDs := make([]string, 0, len(types))
	for i := range types {
		typeIDs = append(typeIDs, types[i].ID)
	}
	localesByType, err := loadPublicProgramEventTypeLocales(ctx, s.db, typeIDs)
	if err != nil {
		return TypeList{}, err
	}
	result := make([]*openv1.ProgramEventType, 0, len(types))
	for i := range types {
		item, err := publicProgramEventTypeProto(types[i], localesByType[types[i].ID], query.AcceptLanguage)
		if err != nil {
			return TypeList{}, err
		}
		result = append(result, item)
	}
	return TypeList{Types: result, Pagination: pagination.BuildResponse(total)}, nil
}
