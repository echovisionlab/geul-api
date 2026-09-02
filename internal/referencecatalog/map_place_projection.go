package referencecatalog

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

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

func resolvedMapPlaceMember(
	members map[string]*commonv1.MemberSummary,
	memberID string,
) (*commonv1.MemberSummary, error) {
	summary := members[memberID]
	if summary == nil || strings.TrimSpace(summary.GetNickname()) == "" {
		return nil, fmt.Errorf("map place references unresolved member %s", memberID)
	}
	return summary, nil
}

// toProto converts a model.MapPlace to protobuf MapPlace
func (s *MapPlaceService) toProto(p *model.MapPlace) *managev1.MapPlace {
	proto := &managev1.MapPlace{
		Id:        p.ID,
		Name:      p.Name,
		Address:   p.Address,
		Lat:       p.Lat,
		Lng:       p.Lng,
		CreatedAt: timestamppb.New(p.CreatedAt),
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}

	if p.AddressComponents != nil {
		proto.AddressComponents = &managev1.AddressComponents{
			Street:     p.AddressComponents.Street,
			City:       p.AddressComponents.City,
			Region:     p.AddressComponents.Region,
			Country:    p.AddressComponents.Country,
			PostalCode: p.AddressComponents.PostalCode,
		}
	}

	if p.ImageFileID != nil && *p.ImageFileID != "" {
		proto.ImageFileId = p.ImageFileID
	}
	if p.GooglePlaceID != nil && *p.GooglePlaceID != "" {
		proto.GooglePlaceId = p.GooglePlaceID
	}
	if p.CreatedByMemberID != nil && *p.CreatedByMemberID != "" {
		proto.CreatedByMemberId = p.CreatedByMemberID
	}
	if p.UpdatedByMemberID != nil && *p.UpdatedByMemberID != "" {
		proto.UpdatedByMemberId = p.UpdatedByMemberID
	}

	return proto
}

func (s *MapPlaceService) toProtoWithMembers(
	ctx context.Context,
	p *model.MapPlace,
	members map[string]*commonv1.MemberSummary,
) (*managev1.MapPlace, error) {
	proto := s.toProto(p)
	if p.ImageFileID != nil && *p.ImageFileID != "" {
		asset, err := s.assets.ReadyRef(ctx, s.db, AssetSource{FileID: *p.ImageFileID, Kind: "map_image"})
		if err != nil {
			return nil, err
		}
		proto.ImageAsset = asset
	}
	if p.CreatedByMemberID != nil && *p.CreatedByMemberID != "" {
		member, err := resolvedMapPlaceMember(members, *p.CreatedByMemberID)
		if err != nil {
			return nil, err
		}
		proto.CreatedByMember = member
	}
	if p.UpdatedByMemberID != nil && *p.UpdatedByMemberID != "" {
		member, err := resolvedMapPlaceMember(members, *p.UpdatedByMemberID)
		if err != nil {
			return nil, err
		}
		proto.UpdatedByMember = member
	}
	return proto, nil
}

func (s *MapPlaceService) resolveMembersForPlaces(
	ctx context.Context,
	places []model.MapPlace,
) (map[string]*commonv1.MemberSummary, error) {
	ids := make([]string, 0, len(places)*2)
	seen := make(map[string]struct{}, len(places)*2)
	for i := range places {
		if places[i].CreatedByMemberID != nil && *places[i].CreatedByMemberID != "" {
			if _, ok := seen[*places[i].CreatedByMemberID]; !ok {
				seen[*places[i].CreatedByMemberID] = struct{}{}
				ids = append(ids, *places[i].CreatedByMemberID)
			}
		}
		if places[i].UpdatedByMemberID != nil && *places[i].UpdatedByMemberID != "" {
			if _, ok := seen[*places[i].UpdatedByMemberID]; !ok {
				seen[*places[i].UpdatedByMemberID] = struct{}{}
				ids = append(ids, *places[i].UpdatedByMemberID)
			}
		}
	}

	if len(ids) == 0 {
		return map[string]*commonv1.MemberSummary{}, nil
	}
	members, err := s.members.Resolve(ctx, s.db, ids)
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

// toBasicProto converts a model.MapPlace to protobuf MapPlaceBasic
func (s *MapPlaceService) toBasicProto(p *model.MapPlace) *managev1.MapPlaceBasic {
	basic := &managev1.MapPlaceBasic{
		Id:      p.ID,
		Name:    p.Name,
		Address: p.Address,
		Lat:     p.Lat,
		Lng:     p.Lng,
	}
	if p.GooglePlaceID != nil && *p.GooglePlaceID != "" {
		basic.GooglePlaceId = p.GooglePlaceID
	}
	return basic
}
