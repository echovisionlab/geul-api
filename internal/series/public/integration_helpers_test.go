//go:build integration

package public_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start public Series integration suite:", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "close public Series integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB { return testutil.NewIntegrationDB(t) }

func seedCanonicalPublicFileFixture(
	t *testing.T,
	db *gorm.DB,
	fileName string,
	mimeType string,
	assetKind string,
) (string, string) {
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
		Extension: extension, MimeType: mimeType, FileSize: &fileSize, SHA256: make([]byte, 32),
		Disposition: "inline", Status: model.PublicAssetStatusReady, ReadyAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	return fileID, assetID
}
