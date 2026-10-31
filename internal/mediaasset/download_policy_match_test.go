package mediaasset

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestRestrictedFileDownloadMemberMatchesAllMembersSegment(t *testing.T) {
	segment := &model.AudienceSegment{
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String(),
	}

	require.True(t, restrictedFileDownloadMemberMatchesSegment(
		restrictedFileDownloadMemberFacts{MemberID: "active-member"},
		segment,
		nil,
		nil,
		nil,
	))
}

func TestResolveRestrictedFileDownloadPermissionsRejectsCorruptStoredRolesAsInternal(t *testing.T) {
	segments := []model.AudienceSegment{{
		ID:          "4b0d7b8e-34f4-4c44-8f5f-4f3a3c8f2d10",
		SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String(),
		Config:      model.AudienceSegmentConfig{AccountRoles: []string{"superuser"}},
	}}

	_, err := resolveRestrictedFileDownloadPermissions(context.Background(), nil, nil, segments)

	// The roles come from a stored segment, so the requester did nothing wrong.
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.ErrorContains(t, err, `invalid account permission "superuser"`)
}
