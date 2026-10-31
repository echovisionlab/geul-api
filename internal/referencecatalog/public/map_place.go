package public

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// MapPlaceService implements the public MapPlaceService
type MapPlaceService struct {
	openv1connect.UnimplementedMapPlaceServiceHandler
	db     *gorm.DB
	assets AssetReader
}

// NewMapPlaceService creates a new public MapPlaceService
func NewMapPlaceService(db *gorm.DB, assets AssetReader) *MapPlaceService {
	if db == nil {
		panic("db is required")
	}
	if assets == nil {
		panic("assets are required")
	}
	return &MapPlaceService{db: db, assets: assets}
}

// GetByIds returns multiple map places by IDs
func (s *MapPlaceService) GetByIds(
	ctx context.Context,
	req *connect.Request[openv1.GetMapPlacesByIdsRequest],
) (*connect.Response[openv1.GetMapPlacesByIdsResponse], error) {
	ids := req.Msg.Ids
	if len(ids) == 0 {
		return connect.NewResponse(&openv1.GetMapPlacesByIdsResponse{
			Places: []*openv1.MapPlace{},
		}), nil
	}

	var places []model.MapPlace
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&places).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoPlaces := make([]*openv1.MapPlace, 0, len(places))
	for _, place := range places {
		protoPlaces = append(protoPlaces, s.toProtoMapPlace(ctx, &place))
	}

	return connect.NewResponse(&openv1.GetMapPlacesByIdsResponse{
		Places: protoPlaces,
	}), nil
}

// toProtoMapPlace converts a model.MapPlace to openv1.MapPlace
func (s *MapPlaceService) toProtoMapPlace(ctx context.Context, place *model.MapPlace) *openv1.MapPlace {
	protoPlace := &openv1.MapPlace{
		Id:        place.ID,
		Name:      place.Name,
		Address:   place.Address,
		Lat:       place.Lat,
		Lng:       place.Lng,
		CreatedAt: timestamppb.New(place.CreatedAt),
	}

	if place.AddressComponents != nil {
		protoPlace.AddressComponents = &openv1.AddressComponents{
			Street:     place.AddressComponents.Street,
			City:       place.AddressComponents.City,
			Region:     place.AddressComponents.Region,
			Country:    place.AddressComponents.Country,
			PostalCode: place.AddressComponents.PostalCode,
		}
	}

	if place.GooglePlaceID != nil && *place.GooglePlaceID != "" {
		protoPlace.GooglePlaceId = place.GooglePlaceID
	}
	if place.ImageFileID != nil {
		protoPlace.ImageAsset = s.assets.ReadyRef(ctx, s.db, referencecatalog.AssetSource{
			FileID:        *place.ImageFileID,
			Kind:          "map_image",
			FallbackKinds: []string{"image"},
		})
	}

	return protoPlace
}
