package referencecatalog

import (
	"context"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
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

// GetMapPlace retrieves a single map place by ID.
func (s *MapPlaceService) GetMapPlace(
	ctx context.Context,
	req *connect.Request[managev1.GetMapPlaceRequest],
) (_ *connect.Response[managev1.MapPlace], returnErr error) {
	defer func() {
		if returnErr == nil || req == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.GetMapPlace", "outcome", "failed", "map_place_id", req.Msg.GetId(), failurecause.Attr(returnErr))
	}()

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var place model.MapPlace
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.Id).First(&place).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("map place", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	users, err := s.resolveMembersForPlaces(ctx, []model.MapPlace{place})
	if err != nil {
		return nil, errs.Internal(err)
	}

	proto, err := s.toProtoWithMembers(ctx, &place, users)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto), nil
}

// GetMapPlacesByIds retrieves multiple map places by IDs (public)
func (s *MapPlaceService) GetMapPlacesByIds(
	ctx context.Context,
	req *connect.Request[managev1.GetMapPlacesByIdsRequest],
) (_ *connect.Response[managev1.GetMapPlacesByIdsResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.GetMapPlacesByIds", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	if len(req.Msg.Ids) == 0 {
		return connect.NewResponse(&managev1.GetMapPlacesByIdsResponse{
			Places: []*managev1.MapPlace{},
		}), nil
	}

	var places []model.MapPlace
	if err := s.db.WithContext(ctx).Where("id IN ?", req.Msg.Ids).Find(&places).Error; err != nil {
		return nil, errs.Internal(err)
	}

	users, err := s.resolveMembersForPlaces(ctx, places)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Create a map for ordered response
	placeMap := make(map[string]*managev1.MapPlace)
	for i := range places {
		proto, err := s.toProtoWithMembers(ctx, &places[i], users)
		if err != nil {
			return nil, err
		}
		placeMap[places[i].ID] = proto
	}

	// Return in the same order as requested
	result := make([]*managev1.MapPlace, 0, len(req.Msg.Ids))
	for _, id := range req.Msg.Ids {
		if p, ok := placeMap[id]; ok {
			result = append(result, p)
		}
	}

	return connect.NewResponse(&managev1.GetMapPlacesByIdsResponse{
		Places: result,
	}), nil
}

// SearchMapPlaces searches map places by name using pgroonga (public)
func (s *MapPlaceService) SearchMapPlaces(
	ctx context.Context,
	req *connect.Request[managev1.SearchMapPlacesRequest],
) (_ *connect.Response[managev1.SearchMapPlacesResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.SearchMapPlaces", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	query := strings.TrimSpace(req.Msg.Query)
	if query == "" {
		return connect.NewResponse(&managev1.SearchMapPlacesResponse{
			Places: []*managev1.MapPlaceBasic{},
		}), nil
	}

	var places []model.MapPlace

	// Use pgroonga for full-text search
	// Escape special characters for pgroonga query
	escapedQuery := escapePgroongaQuery(query)
	err := s.db.WithContext(ctx).
		Where("name &@~ ?", escapedQuery).
		Order("name ASC").
		Limit(limit).
		Find(&places).Error
	if err != nil {
		return nil, errs.Internal(err)
	}

	result := make([]*managev1.MapPlaceBasic, len(places))
	for i := range places {
		result[i] = s.toBasicProto(&places[i])
	}

	return connect.NewResponse(&managev1.SearchMapPlacesResponse{
		Places: result,
	}), nil
}

// ListMapPlacesAdmin lists all map places with pagination (admin)
func (s *MapPlaceService) ListMapPlacesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListMapPlacesAdminRequest],
) (_ *connect.Response[managev1.ListMapPlacesAdminResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.ListMapPlacesAdmin", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	limit := 20
	offset := 0
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = int(req.Msg.Pagination.Limit)
		}
		offset = int(req.Msg.Pagination.Offset)
	}

	query := s.db.WithContext(ctx).Model(&model.MapPlace{})
	var err error
	query, err = mapPlaceFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply sorting with whitelist validation
	query, err = mapPlaceSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	// Fetch with pagination
	var places []model.MapPlace
	if err := query.
		Offset(offset).
		Limit(limit).
		Find(&places).Error; err != nil {
		return nil, errs.Internal(err)
	}

	users, err := s.resolveMembersForPlaces(ctx, places)
	if err != nil {
		return nil, errs.Internal(err)
	}

	result := make([]*managev1.MapPlace, len(places))
	for i := range places {
		result[i], err = s.toProtoWithMembers(ctx, &places[i], users)
		if err != nil {
			return nil, err
		}
	}

	return connect.NewResponse(&managev1.ListMapPlacesAdminResponse{
		Places:     result,
		Pagination: (queryutil.Pagination{Limit: int32(limit), Offset: int32(offset)}).BuildResponse(total),
	}), nil
}

// CreateMapPlace creates a new map place (author+)
