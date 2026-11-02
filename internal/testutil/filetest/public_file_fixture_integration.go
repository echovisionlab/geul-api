//go:build integration

package filetest

import (
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediamime"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
)

// SeedCanonicalPublicFileFixture stores a File and its ready public asset of
// assetKind, and returns their IDs. Digests derive from the IDs so fixtures
// never collide on content identity.
func SeedCanonicalPublicFileFixture(
	t *testing.T,
	db *gorm.DB,
	fileName string,
	mimeType string,
	assetKind string,
) (string, string) {
	t.Helper()
	fileID := uuid.NewString()
	extension := mediamime.GetExtensionFromMime(mimeType)
	fileName = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	now := time.Now().UTC()
	fileDigest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: fileName, MimeType: mimeType, FileSize: 1024,
		Extension: extension, SHA256: fileDigest[:], CreatedAt: now,
	}).Error)
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	fileSize := int64(1024)
	assetDigest := sha256.Sum256([]byte(assetID))
	require.NoError(t, db.Create(&model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: assetKind, ObjectKey: objectKey,
		Extension: extension, MimeType: mimeType, FileSize: &fileSize, SHA256: assetDigest[:],
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}
