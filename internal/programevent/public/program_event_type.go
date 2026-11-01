package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/programevent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
	Types      []TypeEntry
	Pagination programevent.Pagination
}

// ListProgramEventTypes returns one page of active event types in their
// sort order.
func (s *Service) ListProgramEventTypes(ctx context.Context, query ListProgramEventTypesQuery) (TypeList, error) {
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var types []programevent.Type
	var total int64
	var localesByType map[string][]programevent.TypeLocale
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		types, total, err = store.ListActiveTypes(ctx, programevent.ListPage{
			Filters: query.Filters, Limit: pagination.Limit, Offset: pagination.Offset,
		})
		if err != nil {
			return err
		}
		typeIDs := make([]string, 0, len(types))
		for i := range types {
			typeIDs = append(typeIDs, types[i].ID)
		}
		localesByType, err = store.TypeLocales(ctx, typeIDs)
		return err
	}); err != nil {
		return TypeList{}, err
	}
	result := make([]TypeEntry, 0, len(types))
	for i := range types {
		item, err := publicTypeEntry(types[i], localesByType[types[i].ID], query.AcceptLanguage)
		if err != nil {
			return TypeList{}, err
		}
		result = append(result, item)
	}
	return TypeList{Types: result, Pagination: page(pagination, total)}, nil
}
