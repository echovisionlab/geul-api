package filemedia

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/mediamime"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/favicon"
	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type staticFaviconProcessor struct {
	outputs []favicon.Output
	err     error
}

func (p staticFaviconProcessor) Process(context.Context, []byte, string) ([]favicon.Output, error) {
	return p.outputs, p.err
}

func TestSyncSiteSettingFaviconBindingsReplacesAndClearsWholeBundle(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	settingAssets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	firstFileID := seedFaviconSource(t, db, "image/png")
	seedCompleteFaviconDerivatives(t, db, firstFileID)
	secondFileID := seedFaviconSource(t, db, "image/png")
	secondAssets := seedCompleteFaviconDerivatives(t, db, secondFileID)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &firstFileID)
	}))
	assertFaviconBindings(t, db, firstFileID, 7)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &secondFileID)
	}))
	bindings := assertFaviconBindings(t, db, secondFileID, 7)
	wantKeys := []string{"favicon", "favicon_apple180", "favicon_ico", "favicon_manifest192", "favicon_manifest512", "favicon_png16", "favicon_png48"}
	gotKeys := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		gotKeys = append(gotKeys, binding.BindingKey)
	}
	require.ElementsMatch(t, wantKeys, gotKeys)
	require.Equal(t, secondAssets[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32.String()], assetIDForBinding(bindings, "favicon"))

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, nil)
	}))
	var count int64
	require.NoError(t, db.Model(&model.PublicAssetBinding{}).Scopes(func(q *gorm.DB) *gorm.DB { return mediaassetpostgres.WhereBindingKeyFamily(q, "favicon") }).Count(&count).Error)
	require.Zero(t, count)
}

func TestSyncSiteSettingFaviconBindingsIncludesSVG(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	assets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return assets.ReplaceFavicon(t.Context(), tx, &fileID)
	}))
	bindings := assertFaviconBindings(t, db, fileID, 8)
	require.NotEmpty(t, assetIDForBinding(bindings, "favicon_svg"))
}

func TestRequestFaviconAssetsDeletionProtectsActiveThenMarksAllPending(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	settingAssets := sitesettingsadapter.NewAssets("https://cdn.example.com")
	fileID := seedFaviconSource(t, db, "image/svg+xml")
	seedCompleteFaviconDerivatives(t, db, fileID)
	seedReadyFaviconAsset(t, db, &fileID, "svg", "image/svg+xml")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return settingAssets.ReplaceFavicon(t.Context(), tx, &fileID)
	}))
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
		return faviconpostgres.RequestDeletion(t.Context(), tx, fileID)
	}), "active bindings")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := settingAssets.ReplaceFavicon(t.Context(), tx, nil); err != nil {
			return err
		}
		return faviconpostgres.RequestDeletion(t.Context(), tx, fileID)
	}))
	var assets []model.PublicAsset
	require.NoError(t, db.Where("kind = ?", "favicon").Find(&assets).Error)
	require.Len(t, assets, 8)
	for _, asset := range assets {
		require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
		require.NotNil(t, asset.DeleteRequestedAt)
	}
}

func TestRequestFaviconAssetsDeletionProtectsLegacyPNGAndICOSourceAssets(t *testing.T) {
	for _, mimeType := range []string{"image/png", "image/x-icon"} {
		t.Run(mimeType, func(t *testing.T) {
			db := newFaviconBundleUnitDB(t)
			fileID := seedFaviconSource(t, db, mimeType)
			assetID := seedReadyFaviconAsset(t, db, &fileID, mediamime.GetExtensionFromMime(mimeType), mimeType)
			lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "https://cdn.example.com")
			require.NoError(t, lifecycle.BindPublicAsset(t.Context(), mediaasset.Binding{
				AssetID: assetID, OwnerType: "site_settings", OwnerID: "1", BindingKey: "favicon", SourceFileID: &fileID,
			}))
			require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
				return faviconpostgres.RequestDeletion(t.Context(), tx, fileID)
			}), "active bindings")

			require.NoError(t, lifecycle.ReleasePublicAssetBindings(t.Context(), "site_settings", "1", "favicon"))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return faviconpostgres.RequestDeletion(t.Context(), tx, fileID)
			}))
			var asset model.PublicAsset
			require.NoError(t, db.First(&asset, "id = ?", assetID).Error)
			require.Equal(t, model.PublicAssetStatusDeletePending, asset.Status)
		})
	}
}

func TestDeleteFaviconSourceMarksAssetsAndFilePendingBeforeWorkerFinalization(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	createFileAttachmentReferenceTablesForServiceTests(t, db)
	fileID := seedFaviconSource(t, db, "image/png")
	seedCompleteFaviconDerivatives(t, db, fileID)
	svc := withNativeFileUnits(&Service{asyncPublisher: noopAsyncPublisher{}}, db, nativeFilePorts{})
	require.NoError(t, svc.deleteFileRecordByID(t.Context(), fileID))
	var file model.File
	require.NoError(t, db.First(&file, "id = ?", fileID).Error)
	require.NotNil(t, file.DeleteRequestedAt)
	var pendingCount int64
	require.NoError(t, db.Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ?", "favicon", model.PublicAssetStatusDeletePending).
		Count(&pendingCount).Error)
	require.Equal(t, int64(7), pendingCount)
}

func newFaviconBundleUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newServiceUnitDB(t)
	require.NoError(t, db.Exec(`
			CREATE TABLE file_derivative (
			id text PRIMARY KEY, file_id text NOT NULL, type text NOT NULL,
			asset_id text, media_generation_id text, created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(file_id, type)
			)
		`).Error)
	require.NoError(t, db.Exec(`
			CREATE TABLE mesh_optimization_candidate (
				id text PRIMARY KEY, source_file_id text NOT NULL, output_object_id text, output_file_id text,
				status text NOT NULL, selected_at datetime, cancelled_at datetime,
				expires_at datetime, updated_at datetime
			)
		`).Error)
	return db
}

func seedFaviconSource(t *testing.T, db *gorm.DB, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	extension := mediamime.GetExtensionFromMime(mimeType)
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  "favicon." + extension,
		MimeType:  mimeType,
		FileSize:  int64(len(fileID)),
		Extension: extension,
		SHA256:    digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}

func seedFaviconSourceBytes(t *testing.T, db *gorm.DB, mimeType string, data []byte) string {
	t.Helper()
	fileID := uuid.NewString()
	extension := mediamime.GetExtensionFromMime(mimeType)
	digest := sha256.Sum256(data)
	require.NoError(t, db.Create(&model.File{
		ID:        fileID,
		FileName:  "favicon." + extension,
		MimeType:  mimeType,
		FileSize:  int64(len(data)),
		Extension: extension,
		SHA256:    digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}

func seedCompleteFaviconDerivatives(t *testing.T, db *gorm.DB, fileID string) map[string]string {
	t.Helper()
	specs := favicon.RequiredOutputs()
	assets := make(map[string]string, len(specs))
	for _, spec := range specs {
		assetID := seedReadyFaviconAsset(t, db, nil, spec.Extension, spec.MimeType)
		require.NoError(t, db.Table("file_derivative").Create(structured.Fields{
			"id":       uuid.NewString(),
			"file_id":  fileID,
			"type":     spec.DerivativeType.String(),
			"asset_id": assetID,
		}).Error)
		assets[spec.DerivativeType.String()] = assetID
	}
	return assets
}

func seedReadyFaviconAsset(t *testing.T, db *gorm.DB, sourceFileID *string, extension string, mimeType string) string {
	t.Helper()
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	payload := []byte(assetID + extension)
	digest := sha256.Sum256(payload)
	size := int64(len(payload))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.PublicAsset{
		ID:           assetID,
		SourceFileID: sourceFileID,
		Kind:         "favicon",
		ObjectKey:    objectKey,
		Extension:    extension,
		MimeType:     mimeType,
		FileSize:     &size,
		SHA256:       digest[:],
		Disposition:  "inline",
		Status:       model.PublicAssetStatusReady,
		ReadyAt:      &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error)
	return assetID
}

type faviconS3Fixture struct {
	client     *s3.Client
	mu         sync.Mutex
	puts       int
	failPutAt  int
	failDelete bool
	written    []string
	deleted    []string
}

func newFaviconS3Fixture(t *testing.T, fileID string, extension string, source []byte, failPutAt int) *faviconS3Fixture {
	t.Helper()
	fixture := &faviconS3Fixture{failPutAt: failPutAt}
	sourcePath := "/media-bucket/media/" + fileID + "." + extension
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != sourcePath {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(source)))
			_, _ = w.Write(source)
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			fixture.mu.Lock()
			fixture.puts++
			putNumber := fixture.puts
			if failPutAt == 0 || putNumber < failPutAt {
				fixture.written = append(fixture.written, strings.TrimPrefix(r.URL.Path, "/media-bucket/"))
			}
			fixture.mu.Unlock()
			if failPutAt > 0 && putNumber == failPutAt {
				http.Error(w, "injected write failure", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			fixture.mu.Lock()
			failDelete := fixture.failDelete
			fixture.mu.Unlock()
			if failDelete {
				http.Error(w, "injected delete failure", http.StatusInternalServerError)
				return
			}
			fixture.mu.Lock()
			fixture.deleted = append(fixture.deleted, strings.TrimPrefix(r.URL.Path, "/media-bucket/"))
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	fixture.client = s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")),
		HTTPClient:  server.Client(),
		Retryer: func() aws.Retryer {
			return aws.NopRetryer{}
		},
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	return fixture
}

func (f *faviconS3Fixture) writtenKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.written...)
}

func (f *faviconS3Fixture) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func assertFaviconBindings(t *testing.T, db *gorm.DB, sourceFileID string, want int) []model.PublicAssetBinding {
	t.Helper()
	var bindings []model.PublicAssetBinding
	require.NoError(t, db.Where("owner_type = ? AND owner_id = ?", "site_settings", "1").
		Scopes(func(q *gorm.DB) *gorm.DB { return mediaassetpostgres.WhereBindingKeyFamily(q, "favicon") }).
		Order("binding_key ASC").
		Find(&bindings).Error)
	require.Len(t, bindings, want)
	for _, binding := range bindings {
		require.NotNil(t, binding.SourceFileID)
		require.Equal(t, sourceFileID, *binding.SourceFileID)
	}
	return bindings
}

func assetIDForBinding(bindings []model.PublicAssetBinding, key string) string {
	for _, binding := range bindings {
		if binding.BindingKey == key {
			return binding.AssetID
		}
	}
	return ""
}
