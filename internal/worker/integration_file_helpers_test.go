//go:build integration

package worker

import (
	"crypto/sha256"
	"testing"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedWorkerAssetDerivative(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	kind string,
	extension string,
	mimeType string,
) (string, string) {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(assetID))
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO public_asset (
			id, source_file_id, kind, object_key, extension, mime_type, file_size, sha256,
			disposition, status, ready_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 1024, ?, 'inline', 'ready', ?, ?, ?)
	`, assetID, fileID, kind, objectKey, extension, mimeType, digest[:], now, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO file_derivative (file_id, type, asset_id)
		VALUES (?, ?, ?)
	`, fileID, derivativeType.String(), assetID).Error)
	return assetID, objectKey
}

func seedWorkerHLSGenerationDerivative(t *testing.T, db *gorm.DB, fileID string) (string, string) {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(generationID))
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO media_generation (
			id, file_id, kind, object_prefix, manifest_name, manifest_sha256,
			object_count, total_size, status, ready_at, created_at, updated_at
		) VALUES (?, ?, 'hls', ?, 'master.m3u8', ?, 2, 2048, 'ready', ?, ?, ?)
	`, generationID, fileID, objectPrefix, digest[:], now, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO file_derivative (file_id, type, media_generation_id)
		VALUES (?, ?, ?)
	`, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), generationID).Error)
	return generationID, objectPrefix
}

func requireWorkerFilePresent(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
