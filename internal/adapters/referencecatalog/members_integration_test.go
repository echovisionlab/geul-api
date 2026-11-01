//go:build integration

package referencecatalogadapter

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestMemberSummariesExcludeUnresolvedAndNotOnboardedMembersIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	memberID := uuid.NewString()
	members := NewMemberSummaries("https://cdn.example.com")

	resolved, err := members.Resolve(t.Context(), db, []string{memberID})
	require.NoError(t, err)
	require.NotContains(t, resolved, memberID)

	require.NoError(t, db.Create(&model.Member{
		ID: memberID, Nickname: memberID, Onboarded: false, SocialLinks: map[string]string{},
	}).Error)
	resolved, err = members.Resolve(t.Context(), db, []string{memberID})
	require.NoError(t, err)
	require.NotContains(t, resolved, memberID)
}
