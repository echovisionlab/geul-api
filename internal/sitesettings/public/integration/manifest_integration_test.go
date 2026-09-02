//go:build integration

package integration

import (
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestManifestProjectsRuntimeSiteOriginIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	require.NoError(t, db.Exec(`
		UPDATE site_settings
		SET legal_email = ?, support_email = ?, privacy_email = ?
		WHERE id = 1
	`, "legal@example.test", "support@example.test", "privacy@example.test").Error)
	service := newManifestIntegrationService(t, db, "https://cdn.example.com", "https://www.example.test/")

	response, err := service.Get(t.Context(), connect.NewRequest(&openv1.GetRequest{}))
	require.NoError(t, err)
	require.Equal(t, "https://www.example.test", response.Msg.GetSettings().GetSiteOrigin())
	require.Equal(t, "legal@example.test", response.Msg.GetSettings().GetLegalEmail())
	require.Equal(t, "support@example.test", response.Msg.GetSettings().GetSupportEmail())
	require.Equal(t, "privacy@example.test", response.Msg.GetSettings().GetPrivacyEmail())
}

func TestManifestProjectsCurrentSiteAssetsOnCanonicalStaticRoutesIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	const cdnDomain = "https://cdn.example.com"
	service := newManifestIntegrationService(t, db, cdnDomain, "https://www.example.test")

	logoFileID, logoAssetID := seedCanonicalPublicFileFixture(t, db, "logo.png", "image/png", "logo")
	loaderFileID, loaderAssetID := seedCanonicalPublicFileFixture(t, db, "loader.webp", "image/webp", "loader")
	_, ogAssetID := seedCanonicalPublicFileFixture(t, db, "og.webp", "image/webp", "og")
	require.NoError(t, db.Exec(
		`UPDATE site_settings SET logo_light_file_id = ?, site_og_asset_id = ? WHERE id = 1`,
		logoFileID,
		ogAssetID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO site_setting_loader_file (site_setting_id, file_id, position) VALUES (1, ?, 0)`,
		loaderFileID,
	).Error)

	response, err := service.Get(t.Context(), connect.NewRequest(&openv1.GetRequest{}))
	require.NoError(t, err)
	require.Equal(t, cdnDomain+"/asset/"+logoAssetID+"/logo.png", response.Msg.GetSettings().GetLogoLightAsset().GetUrl())
	require.Equal(t, cdnDomain+"/asset/"+ogAssetID+"/og.webp", response.Msg.GetSettings().GetSiteOgAsset().GetUrl())
	require.Len(t, response.Msg.GetSettings().GetLoaderAssets(), 1)
	require.Equal(t, cdnDomain+"/asset/"+loaderAssetID+"/loader.webp", response.Msg.GetSettings().GetLoaderAssets()[0].GetUrl())
}

func newManifestIntegrationService(t *testing.T, db *gorm.DB, cdnDomain, siteOrigin string) *publicsitesettings.ManifestService {
	t.Helper()
	return publicsitesettings.NewManifestService(
		siteOrigin,
		sitesettingsadapter.NewPublicProjection(
			db,
			sitesettingsadapter.NewAssets(cdnDomain),
			sitesettingsadapter.ManifestMenus{},
		),
		testutil.IntegrationSpiceDB(t),
	)
}

func seedCanonicalPublicFileFixture(
	t *testing.T,
	db *gorm.DB,
	fileName string,
	mimeType string,
	kind string,
) (string, string) {
	t.Helper()
	fileID := testutil.IntegrationUUID()
	extension := strings.TrimPrefix(filepath.Ext(fileName), ".")
	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	digest := sha256.Sum256([]byte(fileName + fileID))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, ?, 1024, ?, ?)`,
		fileID,
		baseName,
		mimeType,
		extension,
		digest[:],
	).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         kind,
		Extension:    extension,
		MimeType:     mimeType,
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: digest[:],
	})
	require.NoError(t, err)
	return fileID, asset.ID
}
