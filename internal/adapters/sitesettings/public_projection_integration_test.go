//go:build integration

package sitesettings_test

import (
	"crypto/sha256"
	"testing"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const cdnDomain = "https://cdn.example.com"

// seedFile stores a File with a ready public asset of kind and returns both
// IDs.
func seedFile(t *testing.T, db *gorm.DB, name, extension, mimeType, kind string) (string, string) {
	t.Helper()
	fileID := testutil.IntegrationUUID()
	digest := sha256.Sum256([]byte(name + fileID))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		VALUES (?, ?, ?, 1024, ?, ?)`, fileID, name, mimeType, extension, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), cdnDomain)
	asset, _, err := mediaasset.AllocationResultProto(lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID, Kind: kind, Extension: extension, MimeType: mimeType,
		Disposition: mediaasset.DispositionInline,
	}))
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: digest[:]}))
	require.NoError(t, err)
	return fileID, asset.ID
}

func TestPublicProjectionReadsTheSettingsAndTheirAssetsOnCanonicalRoutesIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	projection := sitesettingsadapter.NewPublicProjection(db, sitesettingsadapter.NewAssets(cdnDomain), sitesettingsadapter.ManifestMenus{})
	logoFileID, logoAssetID := seedFile(t, db, "logo", "png", "image/png", "logo")
	secondLoader, secondLoaderAsset := seedFile(t, db, "second", "webp", "image/webp", "loader")
	firstLoader, firstLoaderAsset := seedFile(t, db, "first", "webp", "image/webp", "loader")
	unreadyLoader := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension)
		VALUES (?, 'unready', 'image/webp', 1024, 'webp')`, unreadyLoader).Error)
	_, ogAssetID := seedFile(t, db, "og", "webp", "image/webp", "og")
	require.NoError(t, db.Exec(`UPDATE site_settings SET logo_full_light_file_id = ?, site_og_asset_id = ?,
		legal_email = 'legal@example.test' WHERE id = 1`, logoFileID, ogAssetID).Error)
	require.NoError(t, db.Exec(`INSERT INTO site_setting_loader_file (site_setting_id, file_id, position)
		VALUES (1, ?, 1), (1, ?, 0), (1, ?, 2)`, secondLoader, firstLoader, unreadyLoader).Error)

	settings, err := projection.Settings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "legal@example.test", settings.LegalEmail)
	require.Equal(t, logoFileID, *settings.LogoFullLightFileID)

	logo := projection.ReadySourceAsset(t.Context(), logoFileID, "logo")
	require.Equal(t, cdnDomain+"/asset/"+logoAssetID+"/logo.png", logo.URL)
	require.Nil(t, projection.ReadySourceAsset(t.Context(), logoFileID, "loader"), "a File serves only the asset kinds it has")
	require.Equal(t, cdnDomain+"/asset/"+ogAssetID+"/og.webp", projection.ReadyAsset(t.Context(), ogAssetID).URL)
	loaders, err := projection.LoaderAssets(t.Context())
	require.NoError(t, err)
	require.Len(t, loaders, 2, "a loader without a ready asset is left out")
	require.Equal(t, []string{firstLoaderAsset, secondLoaderAsset}, []string{loaders[0].ID, loaders[1].ID})
	require.Equal(t, cdnDomain+"/asset/"+firstLoaderAsset+"/loader.webp", loaders[0].URL, "the route names the asset kind")
}
