//go:build integration

package worker

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func TestFaviconCleanupAndBindingSerializeOnPostgresAssetRow(t *testing.T) {
	pg, err := sharedWorkerIntegrationPostgres()
	require.NoError(t, err)
	now := time.Now().UTC()
	cutoff := now.Add(-24 * time.Hour)
	handlers := &Handlers{db: pg.DB}

	t.Run("cleanup lock wins", func(t *testing.T) {
		asset := seedPostgresReadyFaviconAsset(t, pg.DB, now.Add(-25*time.Hour))
		t.Cleanup(func() {
			_ = pg.DB.Where("asset_id = ?", asset.ID).Delete(&model.PublicAssetBinding{}).Error
			_ = pg.DB.Where("id = ?", asset.ID).Delete(&model.PublicAsset{}).Error
		})
		locked := make(chan struct{})
		release := make(chan struct{})
		type cleanupResult struct {
			changed bool
			err     error
		}
		cleanupDone := make(chan cleanupResult, 1)
		go func() {
			changed, cleanupErr := faviconCleanupForIntegration(handlers).MarkDanglingReadyAsset(context.Background(), asset.ID, cutoff, now, func() {
				close(locked)
				<-release
			})
			cleanupDone <- cleanupResult{changed: changed, err: cleanupErr}
		}()
		<-locked
		bindDone := make(chan error, 1)
		go func() {
			bindDone <- mediaasset.NewLifecycle(pg.DB, "").BindPublicAsset(context.Background(), mediaasset.Binding{
				AssetID: asset.ID, OwnerType: "integration", OwnerID: uuid.NewString(), BindingKey: "favicon",
			})
		}()
		close(release)
		cleanup := <-cleanupDone
		require.NoError(t, cleanup.err)
		require.True(t, cleanup.changed)
		require.Error(t, <-bindDone)
	})

	t.Run("binding lock wins", func(t *testing.T) {
		asset := seedPostgresReadyFaviconAsset(t, pg.DB, now.Add(-25*time.Hour))
		t.Cleanup(func() {
			_ = pg.DB.Where("asset_id = ?", asset.ID).Delete(&model.PublicAssetBinding{}).Error
			_ = pg.DB.Where("id = ?", asset.ID).Delete(&model.PublicAsset{}).Error
		})
		tx := pg.DB.Begin()
		require.NoError(t, tx.Error)
		ownerID := uuid.NewString()
		require.NoError(t, mediaasset.NewLifecycle(tx, "").BindPublicAsset(t.Context(), mediaasset.Binding{
			AssetID: asset.ID, OwnerType: "integration", OwnerID: ownerID, BindingKey: "favicon",
		}))
		type cleanupResult struct {
			changed bool
			err     error
		}
		cleanupDone := make(chan cleanupResult, 1)
		go func() {
			changed, cleanupErr := faviconCleanupForIntegration(handlers).MarkDanglingReadyAsset(context.Background(), asset.ID, cutoff, now, nil)
			cleanupDone <- cleanupResult{changed: changed, err: cleanupErr}
		}()
		select {
		case result := <-cleanupDone:
			t.Fatalf("cleanup did not wait for binding transaction: changed=%v err=%v", result.changed, result.err)
		case <-time.After(100 * time.Millisecond):
		}
		require.NoError(t, tx.Commit().Error)
		cleanup := <-cleanupDone
		require.NoError(t, cleanup.err)
		require.False(t, cleanup.changed)
		var status string
		require.NoError(t, pg.DB.Model(&model.PublicAsset{}).Select("status").Where("id = ?", asset.ID).Scan(&status).Error)
		require.Equal(t, model.PublicAssetStatusReady, status)
	})
}

func seedPostgresReadyFaviconAsset(t *testing.T, db *gorm.DB, createdAt time.Time) model.PublicAsset {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "png")
	require.NoError(t, err)
	payload := []byte(assetID)
	digest := sha256.Sum256(payload)
	size := int64(len(payload))
	readyAt := createdAt
	asset := model.PublicAsset{
		ID: assetID, Kind: "favicon", ObjectKey: objectKey, Extension: "png", MimeType: "image/png",
		FileSize: &size, SHA256: digest[:], Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &readyAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	require.NoError(t, db.Create(&asset).Error)
	return asset
}
