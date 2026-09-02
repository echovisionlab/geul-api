package referencecatalogadapter

import (
	"github.com/echovisionlab/geul-api/internal/model"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// PublicMapPlaces adapts Reference Catalog-owned projections to public domain consumers.
type PublicMapPlaces struct{}

func (PublicMapPlaces) Basic(place *model.MapPlace) *openv1.MapPlaceBasic {
	return publicreferencecatalog.ToProtoMapPlaceBasic(place)
}
