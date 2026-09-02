//go:build integration

package sitesettings_test

import (
	"testing"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestResolveSiteOgUsesCanonicalSettingsRowPostgresIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{})
	db := pg.DB
	fileID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension)
		 VALUES (?, 'site-og-background', 'image/png', 1, 'png')`,
		fileID,
	).Error)
	require.NoError(t, db.Exec(
		"UPDATE site_settings SET site_og_background_file_id = ? WHERE id = 1",
		fileID,
	).Error)

	requests, err := sitesettingsadapter.NewRequests().Resolve(t.Context(), db, "site", "", &managev1.OgTargetSelection{
		Target: &managev1.OgTargetSelection_Primary{
			Primary: &managev1.OgPrimaryTarget{},
		},
	})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "default", requests[0].EntityID)
	require.Equal(t, "Home", requests[0].Title)
	require.NotNil(t, requests[0].FeaturedImageFileID)
	require.Equal(t, fileID, *requests[0].FeaturedImageFileID)
}

func TestResolveSiteOgRejectsNonCanonicalIdentityPostgresIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{})
	entityID := "arbitrary"
	_, err := sitesettingsadapter.NewRequests().Resolve(t.Context(), pg.DB, "site", entityID, &managev1.OgTargetSelection{
		Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}},
	})
	require.Error(t, err)
}
