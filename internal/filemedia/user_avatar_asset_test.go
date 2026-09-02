package filemedia

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestPromoteUserAvatarAssetStreamsWebPAndReusesReadyAsset(t *testing.T) {
	db := newUserAvatarAssetUnitDB(t)
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte("verified-avatar"))
	createAvatarSourceFile(t, db, fileID, int64(len("verified-avatar")), digest[:])

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch r.Method {
		case http.MethodPut:
			require.Contains(t, r.URL.Path, "/bucket/asset/")
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "verified-avatar", string(body))
			w.Header().Set("ETag", `"avatar"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			require.Contains(t, r.URL.Path, "/media/")
			w.Header().Set("Content-Length", fmt.Sprint(len("verified-avatar")))
			w.Header().Set("Content-Type", "image/webp")
			_, _ = fmt.Fprint(w, "verified-avatar")
		case http.MethodDelete:
			t.Fatal("successful promotion must not delete the asset object")
		default:
			t.Fatalf("unexpected S3 method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	service := &FileService{
		db:        db,
		s3Bucket:  "bucket",
		cdnDomain: "https://cdn.example.com",
		s3Client:  newAvatarAssetTestS3Client(server),
	}
	ref, err := service.PromoteUserAvatarAsset(t.Context(), fileID)

	require.NoError(t, err)
	require.Equal(t, "webp", ref.GetExtension())
	require.Equal(t, "image/webp", ref.GetMimeType())
	require.True(t, strings.HasSuffix(ref.GetUrl(), "/asset/"+ref.GetAssetId()+"/avatar.webp"))
	require.Equal(t, digest[:], ref.GetSha256())
	require.Equal(t, 2, requestCount)

	reused, err := service.PromoteUserAvatarAsset(t.Context(), fileID)
	require.NoError(t, err)
	require.Equal(t, ref.GetAssetId(), reused.GetAssetId())
	require.Equal(t, 2, requestCount)
}

func TestPromoteUserAvatarAssetMarksAllocationFailedWithoutDeletingSharedObject(t *testing.T) {
	db := newUserAvatarAssetUnitDB(t)
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte("verified-avatar"))
	createAvatarSourceFile(t, db, fileID, int64(len("verified-avatar")), digest[:])

	deleted := false
	failWrite := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			if failWrite {
				http.Error(w, "injected write failure", http.StatusInternalServerError)
				return
			}
			w.Header().Set("ETag", `"avatar"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.Header().Set("Content-Length", fmt.Sprint(len("verified-avatar")))
			w.Header().Set("Content-Type", "image/webp")
			_, _ = fmt.Fprint(w, "verified-avatar")
		case http.MethodDelete:
			deleted = true
		default:
			t.Fatalf("unexpected S3 method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	service := &FileService{
		db:        db,
		s3Bucket:  "bucket",
		cdnDomain: "https://cdn.example.com",
		s3Client:  newAvatarAssetTestS3Client(server),
	}
	ref, err := service.PromoteUserAvatarAsset(t.Context(), fileID)

	require.Error(t, err)
	require.Nil(t, ref)
	require.False(t, deleted)
	var asset model.PublicAsset
	require.NoError(t, db.First(&asset, "source_file_id = ?", fileID).Error)
	require.Equal(t, model.PublicAssetStatusFailed, asset.Status)
	require.NotNil(t, asset.FailedAt)
	require.NotNil(t, asset.FailureReason)

	failWrite = false
	retried, err := service.PromoteUserAvatarAsset(t.Context(), fileID)
	require.NoError(t, err)
	require.Equal(t, asset.ID, retried.GetAssetId())
	var retriedAsset model.PublicAsset
	require.NoError(t, db.First(&retriedAsset, "id = ?", asset.ID).Error)
	require.Equal(t, model.PublicAssetStatusReady, retriedAsset.Status)
	require.Nil(t, retriedAsset.FailedAt)
	require.Nil(t, retriedAsset.FailureReason)
	var count int64
	require.NoError(t, db.Model(&model.PublicAsset{}).Where("source_file_id = ?", fileID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestPromoteUserAvatarAssetResumesAllocatedAsset(t *testing.T) {
	db := newUserAvatarAssetUnitDB(t)
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte("verified-avatar"))
	createAvatarSourceFile(t, db, fileID, int64(len("verified-avatar")), digest[:])
	allocated, _, err := mediaasset.NewLifecycle(db, "https://cdn.example.com").AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "avatar",
		Extension:    "webp",
		MimeType:     "image/webp",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("ETag", `"avatar"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.Header().Set("Content-Length", fmt.Sprint(len("verified-avatar")))
			w.Header().Set("Content-Type", "image/webp")
			_, _ = fmt.Fprint(w, "verified-avatar")
		case http.MethodDelete:
			t.Fatal("resuming a valid allocation must not delete the asset object")
		default:
			t.Fatalf("unexpected S3 method %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	service := &FileService{
		db:        db,
		s3Bucket:  "bucket",
		cdnDomain: "https://cdn.example.com",
		s3Client:  newAvatarAssetTestS3Client(server),
	}
	ref, err := service.PromoteUserAvatarAsset(t.Context(), fileID)

	require.NoError(t, err)
	require.Equal(t, allocated.ID, ref.GetAssetId())
	require.Equal(t, digest[:], ref.GetSha256())
}

func newUserAvatarAssetUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newServiceUnitDB(t)
}

func createAvatarSourceFile(t *testing.T, db *gorm.DB, fileID string, size int64, digest []byte) {
	t.Helper()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, MimeType: "image/webp", FileSize: size, Extension: "webp",
		SHA256: append([]byte(nil), digest...), CreatedAt: time.Now().UTC(),
	}).Error)
}

func newAvatarAssetTestS3Client(server *httptest.Server) *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  server.Client(),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
}
