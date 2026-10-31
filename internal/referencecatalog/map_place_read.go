package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func escapePgroongaQuery(q string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `''`,
		`*`, `\*`,
		`+`, `\+`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(q)
}

// GetMapPlaceQuery names a Map Place.
type GetMapPlaceQuery struct {
	ID string
}

// GetMapPlacesByIDsQuery names Map Places to read in the requested order;
// missing IDs are omitted.
type GetMapPlacesByIDsQuery struct {
	IDs []string
}

// SearchMapPlacesQuery finds Map Places whose name matches Query.
type SearchMapPlacesQuery struct {
	Query string
	// Limit is 10 when not positive and at most 50.
	Limit int32
}

// ListMapPlacesAdminQuery pages Map Places for administrators.
type ListMapPlacesAdminQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	// Limit is 20 when not positive.
	Limit  int32
	Offset int32
}

var mapPlaceSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "updated_at DESC",
}

// GetMapPlace retrieves a single map place by ID.
func (s *Service) GetMapPlace(ctx context.Context, query GetMapPlaceQuery) (_ *managev1.MapPlace, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("map_place_id", query.ID))

	if query.ID == "" {
		return nil, domainerr.Required("id")
	}

	var place model.MapPlace
	if err := s.db.WithContext(ctx).Where("id = ?", query.ID).First(&place).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("map place", query.ID)
		}
		return nil, err
	}
	return s.mapPlaceWithMembers(ctx, &place)
}

// GetMapPlacesByIDs retrieves multiple map places by IDs.
func (s *Service) GetMapPlacesByIDs(ctx context.Context, query GetMapPlacesByIDsQuery) ([]*managev1.MapPlace, error) {
	if len(query.IDs) == 0 {
		return []*managev1.MapPlace{}, nil
	}

	var places []model.MapPlace
	if err := s.db.WithContext(ctx).Where("id IN ?", query.IDs).Find(&places).Error; err != nil {
		return nil, err
	}
	projected, err := s.mapPlacesWithMembers(ctx, places)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*managev1.MapPlace, len(projected))
	for _, place := range projected {
		byID[place.Id] = place
	}

	// Return in the same order as requested
	result := make([]*managev1.MapPlace, 0, len(query.IDs))
	for _, id := range query.IDs {
		if place, ok := byID[id]; ok {
			result = append(result, place)
		}
	}
	return result, nil
}

// SearchMapPlaces searches map places by name using pgroonga.
func (s *Service) SearchMapPlaces(ctx context.Context, query SearchMapPlacesQuery) ([]*managev1.MapPlaceBasic, error) {
	limit := int(query.Limit)
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	text := strings.TrimSpace(query.Query)
	if text == "" {
		return []*managev1.MapPlaceBasic{}, nil
	}

	var places []model.MapPlace
	if err := s.db.WithContext(ctx).
		Where("name &@~ ?", escapePgroongaQuery(text)).
		Order("name ASC").
		Limit(limit).
		Find(&places).Error; err != nil {
		return nil, err
	}

	result := make([]*managev1.MapPlaceBasic, len(places))
	for i := range places {
		result[i] = toBasicProto(&places[i])
	}
	return result, nil
}

// ListMapPlacesAdmin lists all map places with pagination (admin)
func (s *Service) ListMapPlacesAdmin(ctx context.Context, query ListMapPlacesAdminQuery) (Page[*managev1.MapPlace], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spiceDB); err != nil {
		return Page[*managev1.MapPlace]{}, err
	}
	limit, offset := int32(20), query.Offset
	if query.Limit > 0 {
		limit = query.Limit
	}

	scoped, err := mapPlaceFilterConfig.ApplyFilters(s.db.WithContext(ctx).Model(&model.MapPlace{}), query.Filters)
	if err != nil {
		return Page[*managev1.MapPlace]{}, err
	}
	var total int64
	if err := scoped.Count(&total).Error; err != nil {
		return Page[*managev1.MapPlace]{}, err
	}
	scoped, err = mapPlaceSortConfig.ApplySort(scoped, query.Sorts)
	if err != nil {
		return Page[*managev1.MapPlace]{}, err
	}

	var places []model.MapPlace
	if err := scoped.Offset(int(offset)).Limit(int(limit)).Find(&places).Error; err != nil {
		return Page[*managev1.MapPlace]{}, err
	}
	items, err := s.mapPlacesWithMembers(ctx, places)
	if err != nil {
		return Page[*managev1.MapPlace]{}, err
	}
	return Page[*managev1.MapPlace]{
		Items:      items,
		Pagination: (queryutil.Pagination{Limit: limit, Offset: offset}).BuildResponse(total),
	}, nil
}
