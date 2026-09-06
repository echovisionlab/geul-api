package label

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
)

func TestNewRuntimeRequiresOGAuthority(t *testing.T) {
	require.Panics(t, func() { NewRuntime("https://cdn.example.com", nil) })
}

func TestRuntimeBuildsBoundedReadyAssetRefsByAuthorityKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	runtime := NewRuntime(" https://cdn.example.com/ ", &og.Refresher{})
	fileID := uuid.NewString()
	assetID := uuid.NewString()
	size := int64(42)
	ready := model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "logo", Extension: "png",
		MimeType: "image/png", FileSize: &size, SHA256: make([]byte, 32),
		Status: model.PublicAssetStatusReady, ObjectKey: "public/label/logo.png", Disposition: "inline",
	}
	invalid := ready
	invalid.ID = uuid.NewString()
	invalid.SHA256 = []byte("invalid")

	byAsset, err := runtime.assetRefs(db, []model.PublicAsset{ready, invalid}, false)
	require.NoError(t, err)
	require.Len(t, byAsset, 1)
	require.Equal(t, assetID, byAsset[assetID].AssetId)
	require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/logo.png", byAsset[assetID].Url)

	bySource, err := runtime.assetRefs(db, []model.PublicAsset{ready, invalid}, true)
	require.NoError(t, err)
	require.Len(t, bySource, 1)
	require.Equal(t, assetID, bySource[fileID].AssetId)
}

func TestPublicRuntimeUsesCallerTransactionForMediaProjection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE public_asset (
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
		created_at DATETIME,
		updated_at DATETIME
	)`).Error)

	fileID := uuid.NewString()
	assetID := uuid.NewString()
	size := int64(42)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "logo", Extension: "png",
		MimeType: "image/png", FileSize: &size, SHA256: make([]byte, 32),
		Status: model.PublicAssetStatusReady, ObjectKey: "public/label/logo.png", Disposition: "inline",
	}).Error)

	// The adapter's default DB is deliberately absent: public reads must use
	// the transaction supplied by the Label projection boundary.
	runtime := &PublicRuntime{cdnDomain: "https://cdn.example.com"}
	refs, err := runtime.ReadyForSourceFiles(context.Background(), db, []string{fileID}, "logo")
	require.NoError(t, err)
	require.Equal(t, assetID, refs[fileID].AssetId)
}
