package public

import (
	"context"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

// GetMapPlacesByIDsQuery names Map Places to read.
type GetMapPlacesByIDsQuery struct {
	IDs []string
}

// MapPlace is a Map Place with its ready public image.
type MapPlace struct {
	Place referencecatalog.MapPlace
	Image *mediaasset.Reference
}

// GetMapPlacesByIDs returns the public Map Places among IDs.
func (s *Service) GetMapPlacesByIDs(ctx context.Context, query GetMapPlacesByIDsQuery) ([]MapPlace, error) {
	if len(query.IDs) == 0 {
		return []MapPlace{}, nil
	}

	var places []MapPlace
	err := s.units.Read(ctx, func(store Store) error {
		stored, err := store.MapPlacesByID(ctx, query.IDs)
		if err != nil {
			return err
		}
		places = make([]MapPlace, 0, len(stored))
		for _, place := range stored {
			public := MapPlace{Place: place}
			if place.ImageFileID != nil {
				public.Image = store.ReadyAsset(ctx, referencecatalog.AssetSource{
					FileID:        *place.ImageFileID,
					Kind:          "map_image",
					FallbackKinds: []string{"image"},
				})
			}
			places = append(places, public)
		}
		return nil
	})
	return places, err
}
