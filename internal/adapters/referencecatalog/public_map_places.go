package referencecatalogadapter

import (
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// PublicMapPlaces adapts Reference Catalog-owned projections to public domain consumers.
type PublicMapPlaces struct{}

// Basic projects the shared public map-place summary; nil stays nil.
func (PublicMapPlaces) Basic(place *model.MapPlace) *openv1.MapPlaceBasic {
	if place == nil {
		return nil
	}

	result := &openv1.MapPlaceBasic{
		Id:      place.ID,
		Name:    place.Name,
		Address: place.Address,
		Lat:     place.Lat,
		Lng:     place.Lng,
	}
	if place.GooglePlaceID != nil && *place.GooglePlaceID != "" {
		result.GooglePlaceId = place.GooglePlaceID
	}
	return result
}
