package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

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

// GetMapPlace retrieves a single map place by ID.
func (s *Service) GetMapPlace(ctx context.Context, query GetMapPlaceQuery) (_ MapPlaceView, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("map_place_id", query.ID))

	if query.ID == "" {
		return MapPlaceView{}, domainerr.Required("id")
	}

	var view MapPlaceView
	err := s.units.Read(ctx, func(store Store) error {
		place, err := store.MapPlace(ctx, query.ID)
		if errors.Is(err, ErrMapPlaceNotFound) {
			return domainerr.ResourceNotFound("map place", query.ID)
		}
		if err != nil {
			return err
		}
		views, err := mapPlaceViews(ctx, store, []MapPlace{place})
		if err != nil {
			return err
		}
		view = views[0]
		return nil
	})
	return view, err
}

// GetMapPlacesByIDs retrieves multiple map places by IDs.
func (s *Service) GetMapPlacesByIDs(ctx context.Context, query GetMapPlacesByIDsQuery) ([]MapPlaceView, error) {
	if len(query.IDs) == 0 {
		return []MapPlaceView{}, nil
	}

	var projected []MapPlaceView
	err := s.units.Read(ctx, func(store Store) error {
		places, err := store.MapPlacesByID(ctx, query.IDs)
		if err != nil {
			return err
		}
		projected, err = mapPlaceViews(ctx, store, places)
		return err
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]MapPlaceView, len(projected))
	for _, place := range projected {
		byID[place.Place.ID] = place
	}

	// Return in the same order as requested
	result := make([]MapPlaceView, 0, len(query.IDs))
	for _, id := range query.IDs {
		if place, ok := byID[id]; ok {
			result = append(result, place)
		}
	}
	return result, nil
}

// SearchMapPlaces searches map places by name.
func (s *Service) SearchMapPlaces(ctx context.Context, query SearchMapPlacesQuery) ([]MapPlace, error) {
	limit := int(query.Limit)
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	text := strings.TrimSpace(query.Query)
	if text == "" {
		return []MapPlace{}, nil
	}

	var places []MapPlace
	err := s.units.Read(ctx, func(store Store) (err error) {
		places, err = store.SearchMapPlaces(ctx, text, limit)
		return err
	})
	return places, err
}

// ListMapPlacesAdmin lists all map places with pagination (admin)
func (s *Service) ListMapPlacesAdmin(ctx context.Context, query ListMapPlacesAdminQuery) (Page[MapPlaceView], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spiceDB); err != nil {
		return Page[MapPlaceView]{}, err
	}
	page := ListPage{Filters: query.Filters, Sorts: query.Sorts, Limit: 20, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}

	var listed Page[MapPlaceView]
	err := s.units.Read(ctx, func(store Store) error {
		places, total, err := store.ListMapPlaces(ctx, page)
		if err != nil {
			return err
		}
		items, err := mapPlaceViews(ctx, store, places)
		if err != nil {
			return err
		}
		listed = Page[MapPlaceView]{Items: items, Pagination: paginationOf(page.Limit, page.Offset, total)}
		return nil
	})
	return listed, err
}
