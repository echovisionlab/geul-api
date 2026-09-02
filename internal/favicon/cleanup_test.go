package favicon

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestCleanupMarksOnlyOldUnboundReadyFaviconAssets(t *testing.T) {
	db := newCleanupUnitDB(t)
	now := time.Now().UTC()
	dangling := seedReadyAsset(t, db, "favicon", now.Add(-25*time.Hour))
	active := seedReadyAsset(t, db, "favicon", now.Add(-25*time.Hour))
	young := seedReadyAsset(t, db, "favicon", now.Add(-23*time.Hour))
	nonFavicon := seedReadyAsset(t, db, "image", now.Add(-25*time.Hour))
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: active.ID, OwnerType: "site_settings", OwnerID: "1", BindingKey: "favicon:ico",
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	require.NoError(t, NewCleanup(db).MarkDanglingReadyAssets(t.Context(), now.Add(-DanglingAssetRetention), now))

	requireAssetStatus(t, db, dangling.ID, model.PublicAssetStatusDeletePending)
	requireAssetStatus(t, db, active.ID, model.PublicAssetStatusReady)
	requireAssetStatus(t, db, young.ID, model.PublicAssetStatusReady)
	requireAssetStatus(t, db, nonFavicon.ID, model.PublicAssetStatusReady)
}

func newCleanupUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset (
			id text PRIMARY KEY, source_file_id text, kind text NOT NULL,
			object_key text NOT NULL UNIQUE, extension text NOT NULL, mime_type text NOT NULL,
			file_size integer, sha256 blob, disposition text NOT NULL, download_filename text,
			status text NOT NULL, ready_at datetime, delete_requested_at datetime, deleted_at datetime,
			failed_at datetime, failure_reason text, created_at datetime NOT NULL, updated_at datetime NOT NULL
		);
		CREATE TABLE public_asset_binding (
			asset_id text NOT NULL, owner_type text NOT NULL, owner_id text NOT NULL,
			binding_key text NOT NULL, source_file_id text, created_at datetime NOT NULL,
			updated_at datetime NOT NULL, PRIMARY KEY (owner_type, owner_id, binding_key)
		);
	`).Error)
	return db
}

func seedReadyAsset(t *testing.T, db *gorm.DB, kind string, createdAt time.Time) model.PublicAsset {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	size := int64(len(id))
	readyAt := createdAt
	asset := model.PublicAsset{
		ID: id, Kind: kind, ObjectKey: "asset/" + id + ".png", Extension: "png", MimeType: "image/png",
		FileSize: &size, SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &readyAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	require.NoError(t, db.Create(&asset).Error)
	return asset
}

func requireAssetStatus(t *testing.T, db *gorm.DB, assetID, status string) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
	require.Equal(t, status, asset.Status)
}
