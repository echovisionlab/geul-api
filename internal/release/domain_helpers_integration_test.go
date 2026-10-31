//go:build integration

package release

import (
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestRequireDocumentContributorsLocksExistingMembersIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Release contributor")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return requireDocumentContributors(t.Context(), tx, []string{memberID})
	}))
	err := db.Transaction(func(tx *gorm.DB) error {
		return requireDocumentContributors(t.Context(), tx, []string{memberID, "ffffffff-ffff-4fff-bfff-ffffffffffff"})
	})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.EqualError(t, errs.Wrap(err), "invalid_argument: contributor_member_ids: contains a Member that does not exist")
}
