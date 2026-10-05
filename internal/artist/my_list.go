package artist

import (
	"context"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ArtistService implements the ArtistService Connect handler
func (s *ArtistService) ListMyArtists(
	ctx context.Context,
	req *connect.Request[managev1.ListMyArtistsRequest],
) (*connect.Response[managev1.ListMyArtistsResponse], error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.NotAuthenticated()
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return nil, errs.NotAuthenticated()
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errs.NotAuthenticated()
	}
	artistIDs, err := s.spiceDB.LookupResources(ctx, policyv1.Artist.LookupManage(), actor)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if len(artistIDs) == 0 {
		return connect.NewResponse(&managev1.ListMyArtistsResponse{
			Artists:    []*managev1.Artist{},
			Pagination: &commonv1.PaginationResponse{Limit: 20},
		}), nil
	}

	var artists []model.Artist
	var total int64
	query := s.db.WithContext(ctx).Model(&model.Artist{}).Where("id IN ?", artistIDs)

	// Handle label_id filter separately (requires JOIN query)
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" && f.GetValue() != "" {
			query = query.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}

	// Apply filters using FilterConfig
	query, err = ArtistFilterConfig.ApplyFilters(query, remainingFilters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = artistSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&artists).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayArtistSourceLocaleDocuments(ctx, artists); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyArtistOgAssets(ctx, artists)
	if err != nil {
		return nil, err
	}
	readyImages, err := s.runtime.LoadArtistImages(ctx, s.db, collectManageArtistIDs(artists))
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Convert to proto
	protoArtists := make([]*managev1.Artist, len(artists))
	for i, artist := range artists {
		protoArtists[i] = s.toProtoArtist(&artist, nil, manageOgAssetFromReadyMap(readyOgAssets, artist.OgAssetID))
		applyManageArtistImages(protoArtists[i], readyImages[artist.ID])
	}

	return connect.NewResponse(&managev1.ListMyArtistsResponse{
		Artists: protoArtists,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// ==================== Utility ====================

// artistSortConfig defines allowed sort fields for artists
