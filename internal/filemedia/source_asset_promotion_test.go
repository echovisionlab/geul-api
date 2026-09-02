package filemedia

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func TestValidateSourceAssetPromotionFileEnforcesKindMediaContract(t *testing.T) {
	digest := sha256.Sum256([]byte("verified-source"))
	for _, tc := range []struct {
		name      string
		kind      string
		extension string
		mimeType  string
		wantError bool
	}{
		{name: "image webp", kind: "image", extension: "webp", mimeType: "image/webp"},
		{name: "image rejects PDF", kind: "image", extension: "pdf", mimeType: "application/pdf", wantError: true},
		{name: "mesh GLB", kind: "mesh", extension: "glb", mimeType: "model/gltf-binary"},
		{name: "mesh rejects image", kind: "mesh", extension: "webp", mimeType: "image/webp", wantError: true},
		{name: "waveform JSON", kind: "waveform", extension: "json", mimeType: "application/json"},
		{name: "waveform rejects PNG", kind: "waveform", extension: "png", mimeType: "image/png", wantError: true},
		{name: "spectrogram PNG", kind: "spectrogram", extension: "png", mimeType: "image/png"},
		{name: "thumbnail rejects PNG", kind: "thumbnail", extension: "png", mimeType: "image/png", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSourceAssetPromotionFile(model.File{
				MimeType:  tc.mimeType,
				FileSize:  128,
				Extension: tc.extension,
				SHA256:    digest[:],
			}, tc.kind)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPrepareSourceAssetPromotionRejectsDifferentActiveContract(t *testing.T) {
	db := newServiceUnitDB(t)
	require.NoError(t, db.Exec(`
		CREATE UNIQUE INDEX uq_public_asset_source_active_test
		ON public_asset (source_file_id)
		WHERE source_file_id IS NOT NULL AND status <> 'deleted'
	`).Error)

	fileID := uuid.NewString()
	assetID := uuid.NewString()
	downloadFilename := "optimized.glb"
	fileSize := int64(128)
	digest := sha256.Sum256([]byte("verified-source"))
	now := time.Now().UTC()
	existing := model.PublicAsset{
		ID:               assetID,
		SourceFileID:     &fileID,
		Kind:             "attachment",
		ObjectKey:        "asset/" + assetID + ".glb",
		Extension:        "glb",
		MimeType:         "model/gltf-binary",
		FileSize:         &fileSize,
		SHA256:           digest[:],
		Disposition:      "attachment",
		DownloadFilename: &downloadFilename,
		Status:           model.PublicAssetStatusReady,
		ReadyAt:          &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, db.Create(&existing).Error)

	_, _, _, err := prepareSourceAssetPromotion(
		t.Context(),
		db,
		mediaasset.NewLifecycle(db, ""),
		model.File{
			ID:        fileID,
			MimeType:  "model/gltf-binary",
			FileSize:  fileSize,
			Extension: "glb",
			SHA256:    digest[:],
		},
		"mesh",
	)
	require.ErrorContains(t, err, "source file is already promoted with a different public asset contract")

	var assets []model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ?", fileID).Find(&assets).Error)
	require.Len(t, assets, 1)
	require.Equal(t, assetID, assets[0].ID)
	require.Equal(t, "attachment", assets[0].Kind)
	require.Equal(t, "attachment", assets[0].Disposition)
}

func TestSourceAssetPromotionStorageFailureRetainsSharedObjectKeyForRetry(t *testing.T) {
	db := newSourceAssetPromotionUnitDB(t)
	body := []byte("verified public source")
	file := seedSourceAssetPromotionUnitFile(t, db, body)
	store := newSourceAssetPromotionS3Fixture(t, file, body)
	store.failTransfer = true
	service := &FileService{db: db, s3Client: store.client, s3Bucket: "media-bucket", cdnDomain: "https://cdn.example.com"}

	_, err := service.promoteSourceFileToPublicAsset(t.Context(), file.ID, "image")
	require.ErrorContains(t, err, "stream source to public asset")
	var failed model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)
	require.NotNil(t, failed.SourceFileID)
	require.Equal(t, file.ID, *failed.SourceFileID)
	require.Empty(t, store.deletedKeys())

	store.setFailTransfer(false)
	ref, err := service.promoteSourceFileToPublicAsset(t.Context(), file.ID, "image")
	require.NoError(t, err)
	require.Equal(t, failed.ID, ref.GetAssetId())
	var ready model.PublicAsset
	require.NoError(t, db.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
	require.Equal(t, failed.ObjectKey, ready.ObjectKey)
	require.Empty(t, store.deletedKeys())
}

func TestRecoverSourceAssetPromotionAllocationAcceptsCommittedAllocation(t *testing.T) {
	db := newServiceUnitDB(t)
	fileID := uuid.NewString()
	assetID := uuid.NewString()
	now := time.Now().UTC()
	asset := model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: "asset/" + assetID + ".webp",
		Extension: "webp", MimeType: "image/webp", Disposition: "inline",
		Status: model.PublicAssetStatusAllocated, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	commitErr := fmt.Errorf("injected lost allocation commit acknowledgement")

	recovered, target, ready, err := recoverSourceAssetPromotionAllocation(t.Context(), db, model.File{
		ID: fileID, Extension: "webp", MimeType: "image/webp",
	}, "image", commitErr)
	require.NoError(t, err)
	require.False(t, ready)
	require.Equal(t, assetID, recovered.ID)
	require.Equal(t, asset.ObjectKey, target.GetObjectKey())
}

func TestRecoverSourceAssetPromotionCompletionAcceptsCommittedReadyMetadata(t *testing.T) {
	db := newSourceAssetPromotionUnitDB(t)
	sourceFileID := uuid.NewString()
	assetID := uuid.NewString()
	digest := sha256.Sum256([]byte("ready bytes"))
	fileSize := int64(11)
	now := time.Now().UTC()
	require.NoError(t, db.Table("file").Create(structured.Fields{
		"id": sourceFileID, "file_name": "source.webp", "mime_type": "image/webp",
		"file_size": fileSize, "extension": "webp", "sha256": digest[:], "created_at": now,
	}).Error)
	asset := model.PublicAsset{
		ID: assetID, SourceFileID: &sourceFileID, Kind: "image", ObjectKey: "asset/" + assetID + ".webp", Extension: "webp",
		MimeType: "image/webp", FileSize: &fileSize, SHA256: digest[:], Disposition: "inline",
		Status: model.PublicAssetStatusReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	commitErr := fmt.Errorf("injected lost completion commit acknowledgement")

	resolved, err := recoverSourceAssetPromotionCompletion(t.Context(), db, sourceFileID, assetID, fileSize, digest[:], commitErr)
	require.NoError(t, err)
	require.True(t, resolved)
	otherDigest := sha256.Sum256([]byte("other bytes"))
	_, err = recoverSourceAssetPromotionCompletion(t.Context(), db, sourceFileID, assetID, fileSize, otherDigest[:], commitErr)
	require.ErrorContains(t, err, "conflicts with ready metadata")
}

func TestCompleteSourceAssetPromotionRejectsPendingSourceDeletion(t *testing.T) {
	db := newSourceAssetPromotionUnitDB(t)
	body := []byte("verified public source")
	file := seedSourceAssetPromotionUnitFile(t, db, body)
	assetID := uuid.NewString()
	now := time.Now().UTC()
	asset := model.PublicAsset{
		ID: assetID, SourceFileID: &file.ID, Kind: "image", ObjectKey: "asset/" + assetID + ".webp",
		Extension: "webp", MimeType: "image/webp", Disposition: "inline",
		Status: model.PublicAssetStatusAllocated, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	require.NoError(t, db.Table("file").Where("id = ?", file.ID).Update("delete_requested_at", now).Error)

	service := &FileService{db: db}
	err := service.completeSourceAssetPromotion(t.Context(), file.ID, assetID, file.FileSize, file.SHA256)
	require.ErrorContains(t, err, "file is pending deletion")

	var stored model.PublicAsset
	require.NoError(t, db.Where("id = ?", assetID).Take(&stored).Error)
	require.Equal(t, model.PublicAssetStatusAllocated, stored.Status)
}

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

type sourceAssetPromotionS3Fixture struct {
	client          *s3.Client
	mu              sync.Mutex
	objects         map[string][]byte
	mimeTypes       map[string]string
	failTransfer    bool
	transferStarted chan struct{}
	releaseTransfer chan struct{}
	transferOnce    sync.Once
	deleted         []string
}

func (f *sourceAssetPromotionS3Fixture) blockTransfers() (<-chan struct{}, chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferStarted = make(chan struct{})
	f.releaseTransfer = make(chan struct{})
	return f.transferStarted, f.releaseTransfer
}

func newSourceAssetPromotionS3Fixture(t *testing.T, file model.File, body []byte) *sourceAssetPromotionS3Fixture {
	t.Helper()
	fixture := &sourceAssetPromotionS3Fixture{objects: map[string][]byte{}, mimeTypes: map[string]string{}}
	sourceKey, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
	require.NoError(t, err)
	fixture.objects[sourceKey] = append([]byte(nil), body...)
	fixture.mimeTypes[sourceKey] = file.MimeType
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	fixture.client = s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient: server.Client(), Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	return fixture
}

func (f *sourceAssetPromotionS3Fixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/media-bucket/")
	switch r.Method {
	case http.MethodPut:
		f.mu.Lock()
		failTransfer := f.failTransfer
		transferStarted := f.transferStarted
		releaseTransfer := f.releaseTransfer
		f.mu.Unlock()
		if transferStarted != nil {
			f.transferOnce.Do(func() { close(transferStarted) })
			<-releaseTransfer
		}
		if failTransfer {
			http.Error(w, "injected promotion write failure", http.StatusInternalServerError)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read promotion body", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.objects[key] = append([]byte(nil), body...)
		f.mimeTypes[key] = r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"test"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		f.mu.Lock()
		body, ok := f.objects[key]
		mimeType := f.mimeTypes[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Type", mimeType)
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		mimeType := f.mimeTypes[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Content-Type", mimeType)
		_, _ = io.Copy(w, bytes.NewReader(body))
	case http.MethodDelete:
		f.mu.Lock()
		f.deleted = append(f.deleted, key)
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func (f *sourceAssetPromotionS3Fixture) setFailTransfer(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failTransfer = fail
}

func (f *sourceAssetPromotionS3Fixture) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}
