package public

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// GetMapPlacesByIDsQuery names Map Places to read.
type GetMapPlacesByIDsQuery struct {
	IDs []string
}

// GetMapPlacesByIDs returns the public Map Places among IDs.
func (s *Service) GetMapPlacesByIDs(ctx context.Context, query GetMapPlacesByIDsQuery) ([]*openv1.MapPlace, error) {
	if len(query.IDs) == 0 {
		return []*openv1.MapPlace{}, nil
	}

	var places []model.MapPlace
	if err := s.db.WithContext(ctx).Where("id IN ?", query.IDs).Find(&places).Error; err != nil {
		return nil, err
	}

	protoPlaces := make([]*openv1.MapPlace, 0, len(places))
	for i := range places {
		protoPlaces = append(protoPlaces, s.toProtoMapPlace(ctx, &places[i]))
	}
	return protoPlaces, nil
}

// toProtoMapPlace converts a model.MapPlace to openv1.MapPlace
func (s *Service) toProtoMapPlace(ctx context.Context, place *model.MapPlace) *openv1.MapPlace {
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
