package mediaasset

import (
	"context"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/require"
)

func TestRestrictedFileDownloadMemberMatchesAllMembersSegment(t *testing.T) {
	segment := &DownloadSegment{
		SegmentType: DownloadAllMembers,
	}

	require.True(t, restrictedFileDownloadMemberMatchesSegment(
		DownloadMemberFacts{MemberID: "active-member"},
		segment,
		nil,
		nil,
		nil,
	))
}

func TestResolveRestrictedFileDownloadPermissionsRejectsCorruptStoredRolesAsInternal(t *testing.T) {
	segments := []DownloadSegment{{
		ID:          "4b0d7b8e-34f4-4c44-8f5f-4f3a3c8f2d10",
		SegmentType: DownloadMembersByFilter,
		Config:      DownloadSegmentConfig{AccountRoles: []string{"superuser"}},
	}}

	_, err := resolveRestrictedFileDownloadPermissions(context.Background(), nil, nil, segments)

	// The roles come from a stored segment, so the requester did nothing wrong.
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.ErrorContains(t, err, `invalid account permission "superuser"`)
}
