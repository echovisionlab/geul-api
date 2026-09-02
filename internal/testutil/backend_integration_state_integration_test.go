//go:build integration

package testutil

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestBackendIntegrationStateRestoresCommittedDatabaseAndSpiceDBIntegration(t *testing.T) {
	stack, err := StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db := stack.Postgres.DB
	spiceDB := stack.SpiceDBClient
	categoryID := uuid.NewString()
	identityID := uuid.NewString()

	var baselineSiteSettings int64
	require.NoError(t, db.Table("site_settings").Count(&baselineSiteSettings).Error)
	require.Positive(t, baselineSiteSettings)
	require.NoError(t, db.Exec(
		`INSERT INTO category (id, name, slug) VALUES (?::uuid, ?, ?)`,
		categoryID,
		"Committed reset category",
		"committed-reset-"+categoryID,
	).Error)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)

	var committedRows int64
	require.NoError(t, db.Table("category").Where("id = ?::uuid", categoryID).Count(&committedRows).Error)
	require.EqualValues(t, 1, committedRows)
	adminSubjects, err := spiceDB.LookupGlobalSubjects(t.Context(), policyv1.Platform.LookupAdminSubjects())
	require.NoError(t, err)
	require.Len(t, adminSubjects, 1)

	require.NoError(t, ResetBackendIntegrationState(t.Context(), stack))
	require.NoError(t, db.Table("category").Where("id = ?::uuid", categoryID).Count(&committedRows).Error)
	require.Zero(t, committedRows)
	var restoredSiteSettings int64
	require.NoError(t, db.Table("site_settings").Count(&restoredSiteSettings).Error)
	require.Equal(t, baselineSiteSettings, restoredSiteSettings)
	adminSubjects, err = spiceDB.LookupGlobalSubjects(context.Background(), policyv1.Platform.LookupAdminSubjects())
	require.NoError(t, err)
	require.Empty(t, adminSubjects)
}
