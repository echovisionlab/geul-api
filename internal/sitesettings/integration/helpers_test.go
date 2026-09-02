//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"path"
	"strings"
	"testing"

	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newServiceIntegrationDB(t *testing.T) *gorm.DB { return testutil.NewIntegrationDB(t) }

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.NewConcurrentPostIntegrationDB(t)
}

const siteSettingIntegrationAuthorizationID = "1"

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	spiceDB := testutil.IntegrationSpiceDB(t)
	requireSiteSettingIntegrationPolicy(t, spiceDB)
	return spiceDB
}

func requireSiteSettingIntegrationPolicy(t *testing.T, spiceDB *auth.SpiceDBClient) {
	t.Helper()
	touch, err := policyv1.SiteSetting.TouchPolicy(siteSettingIntegrationAuthorizationID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), touch)
	require.NoError(t, err)
	t.Cleanup(func() {
		remove, cleanupErr := policyv1.SiteSetting.DeletePolicy(siteSettingIntegrationAuthorizationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = spiceDB.ApplyRelationships(context.Background(), remove)
		require.NoError(t, cleanupErr)
	})
}

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func seedExternalKratosIdentityWithTraits(t *testing.T, db *gorm.DB, identityID, name string) string {
	return testutil.SeedPostIntegrationIdentity(t, db, identityID, name)
}

func integrationAdminCtxWithIdentityAndSpiceDB(t *testing.T, db *gorm.DB) (context.Context, *auth.SpiceDBClient) {
	t.Helper()
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	requireSiteSettingIntegrationPolicy(t, spiceDB)
	return ctx, spiceDB
}

func newSiteSettingsOGInvalidatorForTest(db *gorm.DB, cdnDomain string) *sitesettingsadapter.Invalidator {
	planner := og.NewPlanner(
		db, cdnDomain, sitesettingsadapter.NewRenderConfig(),
		legaladapter.NewProjection(), sitesettingsadapter.NewProjection(),
	)
	collector := og.NewCollector(
		postadapter.NewRequests(), pageadapter.NewRequests(), seriesadapter.NewRequests(),
		workadapter.NewRequests(),
		formogadapter.NewRequests(), legaladapter.NewRequests(), sitesettingsadapter.NewRequests(),
	)
	return sitesettingsadapter.NewInvalidator(
		planner,
		collector.Collect,
		func(ctx context.Context, tx *gorm.DB, kind string, background *string) ([]og.Request, error) {
			return legaladapter.CurrentRequests(ctx, tx, kind, background)
		},
	)
}

func seedHardCutReadyPublicAsset(
	t *testing.T,
	db *gorm.DB,
	kind string,
	extension string,
	mimeType string,
	sourceFileID *string,
) *commonv1.AssetRef {
	t.Helper()
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: sourceFileID, Kind: kind, Extension: extension, MimeType: mimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: 1024, Sha256: digest[:],
	})
	require.NoError(t, err)
	ref, err := lifecycle.ReadyAssetRef(t.Context(), asset.ID)
	require.NoError(t, err)
	return ref
}

func seedImageBindingUploadedFileFixture(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	id := testutil.IntegrationUUID()
	digest := sha256.Sum256([]byte(key))
	fileName := strings.TrimSuffix(path.Base(key), path.Ext(key))
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
		 VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`,
		id, fileName, digest[:],
	).Error)
	seedHardCutReadyPublicAsset(t, db, "image", "webp", "image/webp", &id)
	return id
}
