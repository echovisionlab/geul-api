//go:build integration

package public

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedCanonicalPublicFileFixture(t *testing.T, db *gorm.DB, fileName, mimeType, assetKind string) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	extension := model.GetExtensionFromMime(mimeType)
	fileName = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: make([]byte, 32), CreatedAt: now,
	}).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	fileSize := int64(1024)
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: assetKind, ObjectKey: objectKey,
		Extension: extension, MimeType: mimeType, FileSize: &fileSize,
		SHA256: make([]byte, 32), Disposition: "inline", Status: model.PublicAssetStatusReady,
		ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}
