package mediaasset_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type cleanupObjectStoreStub struct {
	deleteErr error
	attempts  atomic.Int32
}

func (*cleanupObjectStoreStub) ListObjects(context.Context) ([]mediaasset.StoredObject, error) {
	return nil, nil
}
func (*cleanupObjectStoreStub) DeletePrefix(context.Context, string) error { return nil }
func (s *cleanupObjectStoreStub) DeleteObject(context.Context, string) error {
	s.attempts.Add(1)
	return s.deleteErr
}

type publicAssetCacheStub struct{}

func (publicAssetCacheStub) Prefix(mediaasset.Asset) (string, error) {
	return "cdn.example/asset", nil
}
func (publicAssetCacheStub) PurgePrefixes(context.Context, []string) error { return nil }

type publicAssetProtectorStub struct{}

func (publicAssetProtectorStub) IsPublicAssetProtected(context.Context, mediaasset.Asset) (bool, error) {
	return false, nil
}

func TestDeletePendingRetriesPersistedAssetAfterObjectFailure(t *testing.T) {
	assetStore, _ := mediaassettest.New()
	units := mutationtest.New[mediaasset.CleanupStore](assetStore)
	now := time.Now().UTC()
	asset := mediaasset.Asset{
		ID: uuid.NewString(), Kind: "favicon", ObjectKey: "asset/favicon.png", Extension: "png",
		MimeType: "image/png", Disposition: "inline", Status: mediaasset.AssetDeletePending,
		DeleteRequestedAt: &now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	assetStore.PutAsset(asset)
	store := &cleanupObjectStoreStub{deleteErr: errors.New("object store unavailable")}
	cleanup := mediaasset.NewPublicAssetCleanup(units, store, publicAssetCacheStub{}, publicAssetProtectorStub{})

	require.Error(t, cleanup.DeletePending(t.Context(), now))
	require.Error(t, cleanup.DeletePending(t.Context(), now.Add(time.Minute)))

	persisted, err := assetStore.Asset(t.Context(), asset.ID)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetDeletePending, persisted.Status)
	require.Equal(t, int32(2), store.attempts.Load())
}
