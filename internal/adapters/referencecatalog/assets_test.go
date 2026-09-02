package referencecatalogadapter

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

func TestPublicAssetsPreservesMapImageFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE public_asset (
			id TEXT PRIMARY KEY,
			source_file_id TEXT,
			kind TEXT NOT NULL,
			object_key TEXT NOT NULL,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER,
			sha256 BLOB,
			disposition TEXT NOT NULL,
			download_filename TEXT,
			status TEXT NOT NULL,
			ready_at DATETIME,
			delete_requested_at DATETIME,
			deleted_at DATETIME,
			failed_at DATETIME,
			failure_reason TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error)

	fileID := uuid.NewString()
	assetID := uuid.NewString()
	fileSize := int64(256)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:           assetID,
		SourceFileID: &fileID,
		Kind:         "image",
		ObjectKey:    "asset/" + assetID + "/image.webp",
		Extension:    "webp",
		MimeType:     "image/webp",
		FileSize:     &fileSize,
		SHA256:       make([]byte, 32),
		Disposition:  "inline",
		Status:       model.PublicAssetStatusReady,
		ReadyAt:      &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error)
	olderAssetID := uuid.NewString()
	older := now.Add(-time.Minute)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:           olderAssetID,
		SourceFileID: &fileID,
		Kind:         "map_image",
		ObjectKey:    "asset/" + olderAssetID + "/map_image.webp",
		Extension:    "webp",
		MimeType:     "image/webp",
		FileSize:     &fileSize,
		SHA256:       make([]byte, 32),
		Disposition:  "inline",
		Status:       model.PublicAssetStatusReady,
		ReadyAt:      &older,
		CreatedAt:    older,
		UpdatedAt:    older,
	}).Error)

	ref := NewPublicAssets("https://cdn.example.com").ReadyRef(
		t.Context(),
		db,
		referencecatalog.AssetSource{
			FileID:        fileID,
			Kind:          "map_image",
			FallbackKinds: []string{"image"},
		},
	)

	require.NotNil(t, ref)
	require.Equal(t, assetID, ref.GetAssetId())
	require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/image.webp", ref.GetUrl())
}
