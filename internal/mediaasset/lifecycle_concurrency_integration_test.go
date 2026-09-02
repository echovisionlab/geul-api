//go:build integration

package mediaasset_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMediaAssetLifecycleBindRechecksReadyAfterAssetLockIntegration(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	asset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	var locked model.PublicAsset
	require.NoError(t, blocker.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").
		Where("id = ?", asset.ID).
		Take(&locked).Error)

	result := make(chan error, 1)
	go func() {
		result <- mediaasset.NewLifecycle(db, "").BindPublicAsset(
			context.Background(),
			mediaasset.Binding{
				AssetID: asset.ID, OwnerType: "post", OwnerID: ownerID, BindingKey: "featured_image",
			},
		)
	}()
	requireMediaAssetCallStillWaiting(t, result)
	require.NoError(t, blocker.Model(&model.PublicAsset{}).
		Where("id = ?", asset.ID).
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": time.Now().UTC(),
			"updated_at":          time.Now().UTC(),
		}).Error)
	require.NoError(t, blocker.Commit().Error)

	err := requireMediaAssetCallResult(t, result)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var bindingCount int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ?", "post", ownerID).
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
}

func TestMediaAssetLifecycleRebindFailsClosedWhenBindingChangesWhileWaitingIntegration(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	oldAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	newAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	concurrentAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()
	bindingKey := "featured_image"
	mutationDB, applicationName := newMediaAssetMutationDB(t)
	service := mediaasset.NewLifecycle(mutationDB, "")
	require.NoError(t, service.BindPublicAsset(t.Context(), mediaasset.Binding{
		AssetID: oldAsset.ID, OwnerType: "page", OwnerID: ownerID, BindingKey: bindingKey,
	}))

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	var locked []model.PublicAsset
	require.NoError(t, blocker.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").
		Where("id IN ?", []string{oldAsset.ID, newAsset.ID}).
		Order("id ASC").
		Find(&locked).Error)
	require.Len(t, locked, 2)

	result := make(chan error, 1)
	go func() {
		result <- service.BindPublicAsset(context.Background(), mediaasset.Binding{
			AssetID: newAsset.ID, OwnerType: "page", OwnerID: ownerID, BindingKey: bindingKey,
		})
	}()
	requireMediaAssetMutationWaitingOnLock(t, db, applicationName, result)
	require.NoError(t, blocker.Model(&model.PublicAssetBinding{}).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "page", ownerID, bindingKey).
		Updates(structured.Fields{
			"asset_id":   concurrentAsset.ID,
			"updated_at": time.Now().UTC(),
		}).Error)
	require.NoError(t, blocker.Commit().Error)

	err := requireMediaAssetCallResult(t, result)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var binding model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "page", ownerID, bindingKey,
	).Take(&binding).Error)
	require.Equal(t, concurrentAsset.ID, binding.AssetID)
	requireMediaAssetState(t, db, newAsset.ID, model.PublicAssetStatusReady, false)
}

func TestMediaAssetLifecycleConcurrentFirstBindConvergesIntegration(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	firstAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	secondAsset := seedMediaAssetLifecycleReadyAssetIntegration(t, db)
	ownerID := uuid.NewString()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, assetID := range []string{firstAsset.ID, secondAsset.ID} {
		go func(assetID string) {
			<-start
			results <- mediaasset.NewLifecycle(db, "").BindPublicAsset(
				context.Background(),
				mediaasset.Binding{
					AssetID: assetID, OwnerType: "post", OwnerID: ownerID, BindingKey: "featured_image",
				},
			)
		}(assetID)
	}
	close(start)

	errors := []error{requireMediaAssetCallResult(t, results), requireMediaAssetCallResult(t, results)}
	successes := 0
	failures := 0
	for _, err := range errors {
		if err == nil {
			successes++
			continue
		}
		failures++
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	}
	require.GreaterOrEqual(t, successes, 1)
	require.Equal(t, 2, successes+failures)

	var bindings []model.PublicAssetBinding
	require.NoError(t, db.Where(
		"owner_type = ? AND owner_id = ? AND binding_key = ?", "post", ownerID, "featured_image",
	).Find(&bindings).Error)
	require.Len(t, bindings, 1)
	require.Contains(t, []string{firstAsset.ID, secondAsset.ID}, bindings[0].AssetID)
	requireMediaAssetState(t, db, bindings[0].AssetID, model.PublicAssetStatusReady, false)
	losingAssetID := firstAsset.ID
	if bindings[0].AssetID == firstAsset.ID {
		losingAssetID = secondAsset.ID
	}
	if successes == 2 {
		requireMediaAssetState(t, db, losingAssetID, model.PublicAssetStatusDeletePending, true)
	} else {
		requireMediaAssetState(t, db, losingAssetID, model.PublicAssetStatusReady, false)
	}
}

func seedMediaAssetLifecycleReadyAssetIntegration(t *testing.T, db *gorm.DB) model.PublicAsset {
	t.Helper()
	now := time.Now().UTC()
	fileSize := int64(16)
	assetID := uuid.NewString()
	asset := model.PublicAsset{
		ID:          assetID,
		Kind:        "image",
		ObjectKey:   "asset/" + assetID + ".webp",
		Extension:   "webp",
		MimeType:    "image/webp",
		FileSize:    &fileSize,
		SHA256:      make([]byte, 32),
		Disposition: "inline",
		Status:      model.PublicAssetStatusReady,
		ReadyAt:     &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, db.Create(&asset).Error)
	t.Cleanup(func() {
		_ = db.Where("asset_id = ?", asset.ID).Delete(&model.PublicAssetBinding{}).Error
		_ = db.Where("id = ?", asset.ID).Delete(&model.PublicAsset{}).Error
	})
	return asset
}

func requireMediaAssetState(t *testing.T, db *gorm.DB, assetID string, status string, deletionRequested bool) {
	t.Helper()
	var asset model.PublicAsset
	require.NoError(t, db.Where("id = ?", assetID).Take(&asset).Error)
	require.Equal(t, status, asset.Status)
	if deletionRequested {
		require.NotNil(t, asset.DeleteRequestedAt)
		return
	}
	require.Nil(t, asset.DeleteRequestedAt)
}

func newMediaAssetMutationDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	applicationName := "geul_media_asset_" + testutil.IntegrationUUID()
	db := testutil.NewConcurrentPostIntegrationDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	require.NoError(t, db.Exec(`SELECT set_config('application_name', ?, false)`, applicationName).Error)
	return db, applicationName
}

func requireMediaAssetCallStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "call returned before the Media Asset row lock was released", "error: %v", err)
	default:
	}
}

func requireMediaAssetCallResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Media Asset call did not return after its row lock was released")
		return nil
	}
}

func requireMediaAssetMutationWaitingOnLock(t *testing.T, db *gorm.DB, applicationName string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			require.FailNow(t, "mutation returned before its asset lock was reached", "error: %v", err)
		default:
		}
		var waiting bool
		err := db.Raw(`SELECT EXISTS (
			SELECT 1
			FROM pg_stat_activity
			WHERE application_name = ?
			  AND wait_event_type = 'Lock'
			  AND cardinality(pg_blocking_pids(pid)) > 0
		)`, applicationName).Scan(&waiting).Error
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("media-asset mutation did not reach its asset lock within 5s")
}
