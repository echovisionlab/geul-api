package filemediapublicpostgres_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	filepublicpostgres "github.com/echovisionlab/geul-api/internal/filemedia/public/postgres"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReadyPublicAssetProjectionAndLookupUnit(t *testing.T) {
	db := newMediaDeliveryTestDB(t)
	svc := newTestService(testDeps{DB: db, SpiceDB: &auth.SpiceDBClient{}, CDNDomain: "https://cdn.example.com"})
	fileID, sourceAssetID := seedMediaDeliveryTestAsset(t, db, "image")
	_, localizedAssetID := seedMediaDeliveryTestAsset(t, db, "og")

	byID, err := filepublicpostgres.NewStore(db, filepublicpostgres.Ports{}).ReadyAsset(t.Context(), sourceAssetID)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, byID.AssetID)

	bySources, err := filepublicpostgres.NewStore(db, filepublicpostgres.Ports{}).ReadyAssetsForFiles(t.Context(), []string{"", fileID, fileID}, "image")
	require.NoError(t, err)
	require.Len(t, bySources, 1)
	assert.Equal(t, sourceAssetID, bySources[fileID].AssetID)

	byIDs, err := filepublicpostgres.NewStore(db, filepublicpostgres.Ports{}).ReadyAssets(t.Context(), []string{"", sourceAssetID, sourceAssetID})
	require.NoError(t, err)
	require.Len(t, byIDs, 1)

	ref, err := svc.ResolveOGAsset(t.Context(), &byID.AssetID, nil)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, ref.AssetID)
	assert.Equal(t, "https://cdn.example.com/asset/"+sourceAssetID+"/image.webp", ref.URL)
	assert.Equal(t, int64(256), ref.FileSize)
	assert.Len(t, ref.SHA256, 32)

	resolved, err := svc.ResolveOGAsset(t.Context(), &sourceAssetID, &localizedAssetID)
	require.NoError(t, err)
	assert.Equal(t, localizedAssetID, resolved.AssetID)
	resolved, err = svc.ResolveOGAsset(t.Context(), &sourceAssetID, nil)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, resolved.AssetID)
	missingLocalizedID := uuid.NewString()
	resolved, err = svc.ResolveOGAsset(t.Context(), &sourceAssetID, &missingLocalizedID)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, resolved.AssetID)
	resolved, err = svc.ResolveOGAsset(t.Context(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, resolved)

	missingID := uuid.NewString()
	resolved, err = svc.ResolveOGAsset(t.Context(), &missingID, nil)
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

func TestResolvePublicDisplayMediaNeverFallsBackToPrivateOriginalUnit(t *testing.T) {
	db := newMediaDeliveryTestDB(t)
	fileID, assetID := seedMediaDeliveryTestAsset(t, db, "image")
	svc := newTestService(testDeps{DB: db, SpiceDB: &auth.SpiceDBClient{}, CDNDomain: "https://cdn.example.com", MediaDomain: "https://media.example.com", MediaSecret: "must-not-be-used"})

	ready, err := svc.ResolveDisplayMedia(t.Context(), []string{"", fileID, fileID})
	require.NoError(t, err)
	require.Contains(t, ready, fileID)
	require.Equal(t, assetID, optional.Value(optional.Value(ready[fileID]).Asset).AssetID)
	require.Equal(t, optional.Value(ready[fileID]).Asset, optional.Value(ready[fileID]).Thumbnail)
	require.Nil(t, optional.Value(ready[fileID]).Inline)
	require.Nil(t, optional.Value(ready[fileID]).Download)

	require.NoError(t, db.Table("public_asset").Where("id = ?", assetID).
		Updates(map[string]any{"status": model.PublicAssetStatusFailed, "ready_at": nil}).Error)
	unavailable, err := svc.ResolveDisplayMedia(t.Context(), []string{fileID})
	require.NoError(t, err)
	require.NotContains(t, unavailable, fileID)
}

func newMediaDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE file ( client_media_bundle_id TEXT,
			id TEXT PRIMARY KEY,
			file_name TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			extension TEXT NOT NULL,
			sha256 BLOB NOT NULL,
			duration_seconds INTEGER,
			ingest_slot_id TEXT,
			ingest_attempt_id TEXT,
			delete_requested_at DATETIME,
			created_at DATETIME NOT NULL
		);
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
		);
	`).Error)
	return db
}

func seedMediaDeliveryTestAsset(t *testing.T, db *gorm.DB, kind string) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	assetID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  kind + ".webp",
		MimeType:  "image/webp",
		FileSize:  256,
		Extension: "webp",
		SHA256:    make([]byte, 32),
		CreatedAt: now,
	}).Error)
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	fileSize := int64(256)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:           assetID,
		SourceFileID: &fileID,
		Kind:         kind,
		ObjectKey:    objectKey,
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
	return fileID, assetID
}
