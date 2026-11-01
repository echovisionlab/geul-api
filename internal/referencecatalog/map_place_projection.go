package referencecatalog

import (
	"context"
	"fmt"
	"strings"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MapPlaceView is a Map Place with its ready image and the Members who
// created and last updated it.
type MapPlaceView struct {
	Place           MapPlace
	ImageAsset      *commonv1.AssetRef
	CreatedByMember *MemberSummary
	UpdatedByMember *MemberSummary
}

func normalizeGooglePlaceID(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func resolvedMapPlaceMember(members map[string]MemberSummary, memberID string) (*MemberSummary, error) {
	summary, ok := members[memberID]
	if !ok || strings.TrimSpace(summary.Nickname) == "" {
		return nil, fmt.Errorf("map place references unresolved member %s", memberID)
	}
	return &summary, nil
}

// committedMapPlaceView projects a place a mutation committed, in its own
// snapshot.
func (s *Service) committedMapPlaceView(ctx context.Context, place MapPlace) (MapPlaceView, error) {
	var view MapPlaceView
	err := s.units.Read(ctx, func(store Store) error {
		views, err := mapPlaceViews(ctx, store, []MapPlace{place})
		if err != nil {
			return err
		}
		view = views[0]
		return nil
	})
	return view, err
}

// mapPlaceViews projects places with their images and member summaries. A
// place whose image or member cannot be resolved fails the read.
func mapPlaceViews(ctx context.Context, store Store, places []MapPlace) ([]MapPlaceView, error) {
	members, err := resolveMembersForPlaces(ctx, store, places)
	if err != nil {
		return nil, err
	}
	result := make([]MapPlaceView, len(places))
	for i := range places {
		if result[i], err = mapPlaceView(ctx, store, places[i], members); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func mapPlaceView(ctx context.Context, store Store, place MapPlace, members map[string]MemberSummary) (MapPlaceView, error) {
	view := MapPlaceView{Place: place}
	if place.ImageFileID != nil && *place.ImageFileID != "" {
		asset, err := store.ReadyAsset(ctx, AssetSource{FileID: *place.ImageFileID, Kind: "map_image"})
		if err != nil {
			return MapPlaceView{}, err
		}
		view.ImageAsset = asset
	}
	if place.CreatedByMemberID != nil && *place.CreatedByMemberID != "" {
		member, err := resolvedMapPlaceMember(members, *place.CreatedByMemberID)
		if err != nil {
			return MapPlaceView{}, err
		}
		view.CreatedByMember = member
	}
	if place.UpdatedByMemberID != nil && *place.UpdatedByMemberID != "" {
		member, err := resolvedMapPlaceMember(members, *place.UpdatedByMemberID)
		if err != nil {
			return MapPlaceView{}, err
		}
		view.UpdatedByMember = member
	}
	return view, nil
}

func resolveMembersForPlaces(ctx context.Context, store Store, places []MapPlace) (map[string]MemberSummary, error) {
	ids := make([]string, 0, len(places)*2)
	seen := make(map[string]struct{}, len(places)*2)
	for i := range places {
		for _, memberID := range []*string{places[i].CreatedByMemberID, places[i].UpdatedByMemberID} {
			if memberID == nil || *memberID == "" {
				continue
			}
			if _, ok := seen[*memberID]; !ok {
				seen[*memberID] = struct{}{}
				ids = append(ids, *memberID)
			}
		}
	}

	if len(ids) == 0 {
		return map[string]MemberSummary{}, nil
	}
	members, err := store.MemberSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, memberID := range ids {
		if _, err := resolvedMapPlaceMember(members, memberID); err != nil {
			return nil, err
		}
	}
	return members, nil
}
