package mediaasset

import (
	"context"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/require"
)

func TestRestrictedFileDownloadMemberMatchesAllMembersCriteria(t *testing.T) {
	criteria := &DownloadCriteria{
		Kind: DownloadAllMembers,
	}

	require.True(t, restrictedFileDownloadMemberMatchesCriteria(
		DownloadMemberFacts{MemberID: "active-member"},
		criteria,
		nil,
		nil,
		nil,
	))
}

func TestResolveRestrictedFileDownloadPermissionsRejectsCorruptStoredRolesAsInternal(t *testing.T) {
	criteriaSet := []DownloadCriteria{{
		ID:     "4b0d7b8e-34f4-4c44-8f5f-4f3a3c8f2d10",
		Kind:   DownloadMembersByFilter,
		Config: DownloadCriteriaConfig{AccountRoles: []string{"superuser"}},
	}}

	_, err := resolveRestrictedFileDownloadPermissions(context.Background(), nil, nil, criteriaSet)

	// The roles come from a stored criteria, so the requester did nothing wrong.
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
	require.ErrorContains(t, err, `invalid account permission "superuser"`)
}
