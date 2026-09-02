package public

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReadyPublicAssetProjectionAndLookupUnit(t *testing.T) {
	db := newMediaDeliveryTestDB(t)
	fileID, sourceAssetID := seedMediaDeliveryTestAsset(t, db, "image")
	_, localizedAssetID := seedMediaDeliveryTestAsset(t, db, "og")

	byID, err := loadReadyPublicAssetByID(t.Context(), db, sourceAssetID)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, byID.AssetID)

	bySources, err := loadReadyPublicAssetsForSourceFiles(t.Context(), db, []string{"", fileID, fileID}, "image")
	require.NoError(t, err)
	require.Len(t, bySources, 1)
	assert.Equal(t, sourceAssetID, bySources[fileID].AssetID)

	byIDs, err := loadReadyPublicAssetsByIDs(t.Context(), db, []string{"", sourceAssetID, sourceAssetID})
	require.NoError(t, err)
	require.Len(t, byIDs, 1)

	ref, err := projectReadyPublicAsset("https://cdn.example.com/", byID)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, ref.GetAssetId())
	assert.Equal(t, "https://cdn.example.com/asset/"+sourceAssetID+"/image.webp", ref.GetUrl())
	assert.Equal(t, int64(256), ref.GetFileSize())
	assert.Len(t, ref.GetSha256(), 32)

	resolved, err := resolvedOgAssetRef(t.Context(), db, "https://cdn.example.com", &sourceAssetID, &localizedAssetID)
	require.NoError(t, err)
	assert.Equal(t, localizedAssetID, resolved.GetAssetId())
	resolved, err = resolvedOgAssetRef(t.Context(), db, "https://cdn.example.com", &sourceAssetID, nil)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, resolved.GetAssetId())
	missingLocalizedID := uuid.NewString()
	resolved, err = resolvedOgAssetRef(t.Context(), db, "https://cdn.example.com", &sourceAssetID, &missingLocalizedID)
	require.NoError(t, err)
	assert.Equal(t, sourceAssetID, resolved.GetAssetId())
	resolved, err = resolvedOgAssetRef(t.Context(), db, "https://cdn.example.com", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, resolved)

	missingID := uuid.NewString()
	resolved, err = resolvedOgAssetRef(t.Context(), db, "https://cdn.example.com", &missingID, nil)
	require.NoError(t, err)
	assert.Nil(t, resolved)
}

func TestResolvePublicDisplayMediaNeverFallsBackToPrivateOriginalUnit(t *testing.T) {
	db := newMediaDeliveryTestDB(t)
	fileID, assetID := seedMediaDeliveryTestAsset(t, db, "image")
	svc := &FileService{
		db: db, cdnDomain: "https://cdn.example.com",
		mediaDomain: "https://media.example.com", mediaSecret: "must-not-be-used",
	}

	ready, err := svc.ResolvePublicDisplayMedia(t.Context(), []string{"", fileID, fileID})
	require.NoError(t, err)
	require.Contains(t, ready, fileID)
	require.Equal(t, assetID, ready[fileID].GetAsset().GetAssetId())
	require.Equal(t, ready[fileID].GetAsset(), ready[fileID].GetThumbnail())
	require.Nil(t, ready[fileID].GetInline())
	require.Nil(t, ready[fileID].GetDownload())

	require.NoError(t, db.Table("public_asset").Where("id = ?", assetID).
		Updates(map[string]any{"status": model.PublicAssetStatusFailed, "ready_at": nil}).Error)
	unavailable, err := svc.ResolvePublicDisplayMedia(t.Context(), []string{fileID})
	require.NoError(t, err)
	require.NotContains(t, unavailable, fileID)
}

func TestReadyPublicAssetProjectionRejectsIncompleteMetadataUnit(t *testing.T) {
	base := readyPublicAssetRow{
		AssetID:     uuid.NewString(),
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		FileSize:    1,
		SHA256:      make([]byte, 32),
		Disposition: "inline",
	}

	for name, mutate := range map[string]func(*readyPublicAssetRow){
		"missing id":        func(row *readyPublicAssetRow) { row.AssetID = "" },
		"missing extension": func(row *readyPublicAssetRow) { row.Extension = "" },
		"missing mime":      func(row *readyPublicAssetRow) { row.MimeType = "" },
		"missing size":      func(row *readyPublicAssetRow) { row.FileSize = 0 },
		"invalid digest":    func(row *readyPublicAssetRow) { row.SHA256 = []byte{1} },
		"mime mismatch":     func(row *readyPublicAssetRow) { row.Extension = "png" },
		"bad disposition":   func(row *readyPublicAssetRow) { row.Disposition = "public" },
		"missing filename": func(row *readyPublicAssetRow) {
			row.Disposition = "attachment"
			row.DownloadFilename = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			row := base
			mutate(&row)
			_, err := projectReadyPublicAsset("https://cdn.example.com", row)
			require.Error(t, err)
		})
	}

	attachment := base
	attachment.Disposition = "attachment"
	filename := "asset.webp"
	attachment.DownloadFilename = &filename
	_, err := projectReadyPublicAsset("", attachment)
	require.ErrorContains(t, err, "cannot be emitted")
}

func TestCanonicalExpiringMediaRefsUsePurposePolicyUnit(t *testing.T) {
	fileID := uuid.NewString()
	before := time.Now().UTC()
	downloadTTL := 3 * time.Minute
	download, err := buildExpiringFileRef(
		"https://media.example.com",
		"media-secret",
		fileID,
		"flac",
		"audio/flac",
		nil,
		mediaauth.PurposeDownload,
		downloadTTL,
	)
	require.NoError(t, err)
	assert.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, download.GetPurpose())
	assert.Contains(t, download.GetUrl(), "/media/")
	assert.WithinDuration(t, before.Add(downloadTTL), download.GetExpiresAt().AsTime(), 2*time.Second)

	generationID := uuid.NewString()
	playback, err := buildPublicHLSRef(
		"https://media.example.com",
		readyMediaGenerationRow{FileID: fileID, GenerationID: generationID, ManifestName: "master.m3u8"},
	)
	require.NoError(t, err)
	assert.Equal(t, generationID, playback.GetGenerationId())
	assert.Equal(t, "https://media.example.com/media/"+fileID+"/hls/"+generationID+"/master.m3u8", playback.GetUrl())
	assert.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_UNSPECIFIED, mediaDeliveryPurpose(mediaauth.Purpose("unknown")))
	assert.Equal(t, "/asset/id/image.webp", joinPublicCDNPath("", "/asset/id/image.webp"))
	assert.Equal(t, "https://cdn.example.com/asset/id/image.webp", joinPublicCDNPath("cdn.example.com/", "asset/id/image.webp"))
}

func newMediaDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
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
