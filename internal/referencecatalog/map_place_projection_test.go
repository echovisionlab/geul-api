package referencecatalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type mapPlaceMemberSummariesStub map[string]*commonv1.MemberSummary

func (members mapPlaceMemberSummariesStub) Resolve(
	context.Context,
	*gorm.DB,
	[]string,
) (map[string]*commonv1.MemberSummary, error) {
	return members, nil
}

func TestMapPlaceMemberProjectionFailsClosedWhenMemberIsUnresolved(t *testing.T) {
	memberID := "unresolved-member"
	place := model.MapPlace{CreatedByMemberID: &memberID, UpdatedByMemberID: &memberID}
	service := &MapPlaceService{members: mapPlaceMemberSummariesStub{}}

	_, err := service.resolveMembersForPlaces(t.Context(), []model.MapPlace{place})
	require.ErrorContains(t, err, "map place references unresolved member "+memberID)
	_, err = service.toProtoWithMembers(t.Context(), &place, map[string]*commonv1.MemberSummary{})
	require.ErrorContains(t, err, "map place references unresolved member "+memberID)
}
