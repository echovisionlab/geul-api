package mediaasset

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

type cleanupObjectStoreStub struct {
	deleteErr error
	attempts  atomic.Int32
}

func (*cleanupObjectStoreStub) ListObjects(context.Context) ([]StoredObject, error) { return nil, nil }
func (*cleanupObjectStoreStub) DeletePrefix(context.Context, string) error          { return nil }
func (s *cleanupObjectStoreStub) DeleteObject(context.Context, string) error {
	s.attempts.Add(1)
	return s.deleteErr
}

type publicAssetCacheStub struct{}

func (publicAssetCacheStub) Prefix(model.PublicAsset) (string, error) {
	return "cdn.example/asset", nil
}
func (publicAssetCacheStub) PurgePrefixes(context.Context, []string) error { return nil }

type publicAssetProtectorStub struct{}

func (publicAssetProtectorStub) IsPublicAssetProtected(context.Context, *gorm.DB, model.PublicAsset) (bool, error) {
	return false, nil
}

func TestDeletePendingRetriesPersistedAssetAfterObjectFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE public_asset (
		id text PRIMARY KEY, source_file_id text, kind text NOT NULL,
		object_key text NOT NULL UNIQUE, extension text NOT NULL, mime_type text NOT NULL,
		file_size integer, sha256 blob, disposition text NOT NULL, download_filename text,
		status text NOT NULL, ready_at datetime, delete_requested_at datetime, deleted_at datetime,
		failed_at datetime, failure_reason text, created_at datetime NOT NULL, updated_at datetime NOT NULL
	); CREATE TABLE public_asset_binding (
		asset_id text NOT NULL, owner_type text NOT NULL, owner_id text NOT NULL,
		binding_key text NOT NULL, PRIMARY KEY (owner_type, owner_id, binding_key)
	)`).Error)
	now := time.Now().UTC()
	asset := model.PublicAsset{
		ID: uuid.NewString(), Kind: "favicon", ObjectKey: "asset/favicon.png", Extension: "png",
		MimeType: "image/png", Disposition: "inline", Status: model.PublicAssetStatusDeletePending,
		DeleteRequestedAt: &now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	store := &cleanupObjectStoreStub{deleteErr: errors.New("object store unavailable")}
	cleanup := NewPublicAssetCleanup(db, store, publicAssetCacheStub{}, publicAssetProtectorStub{})

	require.Error(t, cleanup.DeletePending(t.Context(), now))
	require.Error(t, cleanup.DeletePending(t.Context(), now.Add(time.Minute)))

	var persisted model.PublicAsset
	require.NoError(t, db.First(&persisted, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusDeletePending, persisted.Status)
	require.Equal(t, int32(2), store.attempts.Load())
}
