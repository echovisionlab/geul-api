//go:build integration

package sitesettingspostgres_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sync/atomic"
	"testing"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type settingsBeginCounter struct {
	gorm.ConnPool
	begins atomic.Int32
}

func (p *settingsBeginCounter) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	p.begins.Add(1)
	return p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, options)
}

type failedSettingsAsset struct {
	sitesettingspostgres.Assets
	fileID   string
	failures atomic.Int32
}

func (a *failedSettingsAsset) ReadyRef(ctx context.Context, db *gorm.DB, fileID, kind string) (*mediaasset.Reference, error) {
	if fileID == a.fileID {
		a.failures.Add(1)
		return nil, db.WithContext(ctx).Exec("SELECT 1 / 0").Error
	}
	return a.Assets.ReadyRef(ctx, db, fileID, kind)
}

func TestAdminSettingsUsesOneSnapshotAndPoolAssetsSurviveSQLFailureIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		require.PanicsWithValue(t, "site settings asset reader requires a connection pool", func() {
			sitesettingspostgres.NewAssetReader(tx, sitesettingsadapter.NewAssets(cdnDomain))
		})
		return nil
	}))
	good, goodAssetID := seedSettingsAsset(t, db, "image")
	failed, _ := seedSettingsAsset(t, db, "image")
	loader, _ := seedSettingsAsset(t, db, "loader")
	require.NoError(t, db.Exec("INSERT INTO site_setting_loader_file (site_setting_id, file_id, position) VALUES (1, ?, 0)", loader).Error)
	require.NoError(t, db.Exec(`UPDATE site_settings SET
 logo_full_light_file_id = ?, logo_full_dark_file_id = ?,
 logo_symbol_light_file_id = ?, logo_symbol_dark_file_id = ?,
 logo_email_light_file_id = ?, logo_email_dark_file_id = ?,
 favicon_file_id = ?, site_og_background_file_id = ?,
 privacy_og_background_file_id = ?, terms_og_background_file_id = ? WHERE id = 1`,
		failed, good, good, good, good, good, good, good, good, good).Error)
	counter := &settingsBeginCounter{ConnPool: db.Statement.ConnPool}
	pool := db.Session(&gorm.Session{NewDB: true, Context: t.Context()})
	pool.Statement.ConnPool = counter
	assets := &failedSettingsAsset{Assets: sitesettingsadapter.NewAssets(cdnDomain), fileID: failed}
	service, err := sitesettings.NewService(sitesettings.Deps{
		Assets: sitesettingspostgres.NewAssetReader(pool, assets), Units: newUnits(t, pool),
		SpiceDB: authztest.AllowAll(), Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{},
	})
	require.NoError(t, err)
	all, err := service.GetSettings(adminContext(t))
	require.NoError(t, err)
	require.EqualValues(t, 1, counter.begins.Load(), "optional assets must not start transactions in either settings or mediaasset")
	require.EqualValues(t, 1, assets.failures.Load())
	require.Nil(t, all.LogoFullLight)
	require.Len(t, all.LoaderAssets, 1)
	require.Equal(t, loader, all.LoaderAssets[0].FileID)
	for _, asset := range []*sitesettings.Asset{
		all.LogoFullDark, all.LogoSymbolLight, all.LogoSymbolDark, all.LogoEmailLight, all.LogoEmailDark,
		all.SiteOgBackground, all.PrivacyOgBackground, all.TermsOgBackground,
	} {
		require.NotNil(t, asset)
		require.Equal(t, goodAssetID, asset.ID)
	}
	require.Nil(t, all.Favicon)
	require.Nil(t, all.FaviconSet)
}

func seedSettingsAsset(t *testing.T, db *gorm.DB, kind string) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256)
 VALUES (?, 'settings image', 'image/png', 1024, 'png', ?)`, fileID, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), cdnDomain)
	asset, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID, Kind: kind, Extension: "png", MimeType: "image/png", Disposition: mediaasset.DispositionInline,
	})
	require.NoError(t, err)
	_, err = lifecycle.CompletePublicAsset(t.Context(), &mediaasset.WrittenAsset{AssetID: asset.ID, FileSize: 1024, SHA256: digest[:]})
	require.NoError(t, err)
	return fileID, asset.ID
}
