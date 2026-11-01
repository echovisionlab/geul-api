package filemedia

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newSourceAssetPromotionUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newServiceUnitDB(t)
	require.NoError(t, db.Exec(`
		CREATE UNIQUE INDEX uq_public_asset_source_active_test
		ON public_asset (source_file_id)
		WHERE source_file_id IS NOT NULL AND status <> 'deleted'
	`).Error)
	return db
}

func seedSourceAssetPromotionUnitFile(t *testing.T, db *gorm.DB, body []byte) model.File {
	t.Helper()
	digest := sha256.Sum256(body)
	file := model.File{
		ID: uuid.NewString(), FileName: "source", MimeType: "image/webp", FileSize: int64(len(body)),
		Extension: "webp", SHA256: digest[:], CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Table("file").Create(structured.Fields{
		"id": file.ID, "file_name": file.FileName, "mime_type": file.MimeType,
		"file_size": file.FileSize, "extension": file.Extension, "sha256": file.SHA256,
		"created_at": file.CreatedAt,
	}).Error)
	return file
}
