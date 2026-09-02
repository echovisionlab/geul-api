package filemedia

import (
	"context"
	"fmt"
	"image/color"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestVerifyMultipartCompletionTransientFetchFailureKeepsFinalizing(t *testing.T) {
	db := newMultipartCompletionRetryDB(t)
	service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary storage failure", http.StatusServiceUnavailable)
	})
	service.db = db

	completion := insertFinalizingCompletion(t, db, 10)
	err := service.verifyMultipartCompletionObject(context.Background(), completion)
	require.Error(t, err)
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
}

func TestCompleteMultipartObjectTransientFailureKeepsFinalizing(t *testing.T) {
	db := newMultipartCompletionRetryDB(t)
	service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary complete failure", http.StatusServiceUnavailable)
	})
	service.db = db

	completion := insertFinalizingCompletion(t, db, 10)
	completion.verifiedMime = "audio/wav"
	err := service.completeMultipartObject(context.Background(), completion)
	require.Error(t, err)
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
}

func TestCompleteMultipartObjectRepairablePartFailureKeepsFinalizing(t *testing.T) {
	for _, code := range []string{"InvalidPart", "InvalidPartOrder", "EntityTooSmall"} {
		t.Run(code, func(t *testing.T) {
			db := newMultipartCompletionRetryDB(t)
			service := newObjectVerificationService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>repairable part list</Message></Error>", code)
			})
			service.db = db

			completion := insertFinalizingCompletion(t, db, 10)
			completion.verifiedMime = "audio/wav"
			err := service.completeMultipartObject(context.Background(), completion)
			require.Error(t, err)
			require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
		})
	}
}

func TestVerifyMultipartCompletionSizeMismatchFailsAndDeletesObject(t *testing.T) {
	var deleteRequests atomic.Int32
	db := newMultipartCompletionRetryDB(t)
	service := newObjectVerificationService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", "5")
			w.Header().Set("Content-Type", "image/png")
		case http.MethodDelete:
			deleteRequests.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	})
	service.db = db

	completion := insertFinalizingCompletion(t, db, 10)
	completion.verifiedMime = "image/png"
	err := service.verifyMultipartCompletionObject(context.Background(), completion)
	require.Error(t, err)
	require.Equal(t, model.UploadSessionStatusFailed, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
	require.EqualValues(t, 1, deleteRequests.Load())
}

func TestPersistMultipartCompletionDatabaseFailureKeepsFinalizing(t *testing.T) {
	db := newMultipartCompletionRetryDB(t)
	service := &FileService{db: db}
	completion := insertFinalizingCompletion(t, db, 10)
	completion.uploadType = managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO
	completion.session.FileName = "field-recording.wav"
	completion.session.DetectedMime = new("audio/wav")

	err := service.persistMultipartCompletion(context.Background(), completion)
	require.Error(t, err)
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
}

func TestMarkMultipartCompletionFailedCannotOverwriteNonFinalizingState(t *testing.T) {
	db := newMultipartCompletionRetryDB(t)
	service := &FileService{db: db}
	completion := insertFinalizingCompletion(t, db, 10)

	require.NoError(t, db.Model(&model.UploadSession{}).
		Where("upload_id = ?", completion.session.UploadID).
		Update("status", model.UploadSessionStatusAborted).Error)
	service.markMultipartCompletionFailed(context.Background(), completion.session.UploadID)

	require.Equal(t, model.UploadSessionStatusAborted, loadMultipartCompletionStatus(t, db, completion.session.UploadID))
}

func TestMultipartPublicAssetPromotionRetryPreservesVerifiedSourceAndFinalizingSession(t *testing.T) {
	body := []byte("verified public source")
	db := newSourceAssetPromotionUnitDB(t)
	createMultipartPromotionRetrySessionTable(t, db)
	file := seedSourceAssetPromotionUnitFile(t, db, body)
	store := newSourceAssetPromotionS3Fixture(t, file, body)
	store.setFailTransfer(true)
	uploadID := uuid.NewString()
	insertMultipartPromotionRetrySession(t, db, uploadID, file.ID)
	service := &FileService{
		db: db, s3Client: store.client, s3Bucket: "media-bucket",
		cdnDomain: "https://cdn.example.com", mediaSecret: "test-media-secret",
	}
	completion := &multipartCompletion{
		session: model.UploadSession{
			UploadID: uploadID, FileID: file.ID, FileName: file.FileName,
			FileSize: file.FileSize, Status: model.UploadSessionStatusFinalizing,
		},
		uploadType:   managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
		verifiedMime: file.MimeType,
		objectKey:    "media/" + file.ID + "." + file.Extension,
	}

	err := service.attachOrProjectMultipartCompletion(t.Context(), completion)
	require.ErrorContains(t, err, "stream source to public asset")
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, uploadID))
	var source model.File
	require.NoError(t, db.Where("id = ?", file.ID).Take(&source).Error)
	require.Nil(t, source.DeleteRequestedAt)
	require.Empty(t, store.deletedKeys())
	var failed model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ?", file.ID).Take(&failed).Error)
	require.Equal(t, model.PublicAssetStatusFailed, failed.Status)

	store.setFailTransfer(false)
	require.NoError(t, service.attachOrProjectMultipartCompletion(t.Context(), completion))
	require.NotNil(t, completion.promotedAsset)
	require.Equal(t, failed.ID, completion.promotedAsset.GetAssetId())
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, uploadID))
	require.Empty(t, store.deletedKeys())
	var ready model.PublicAsset
	require.NoError(t, db.Where("id = ?", failed.ID).Take(&ready).Error)
	require.Equal(t, model.PublicAssetStatusReady, ready.Status)
}

func TestMultipartFaviconCommitAmbiguityPreservesSourceAndFinalizingSession(t *testing.T) {
	db := newFaviconBundleUnitDB(t)
	createMultipartPromotionRetrySessionTable(t, db)
	source := faviconTestPNG(t, 151, 152, color.NRGBA{R: 200, A: 255})
	fileID := seedFaviconSourceBytes(t, db, "image/png", source)
	store := newFaviconS3Fixture(t, fileID, "png", source, 0)
	uploadID := uuid.NewString()
	insertMultipartPromotionRetrySession(t, db, uploadID, fileID)
	service := &FileService{
		db: db, s3Client: store.client, s3Bucket: "media-bucket",
		cdnDomain: "https://cdn.example.com", mediaSecret: "test-media-secret",
		faviconProcessor:          staticFaviconProcessor{outputs: faviconTestGeneratedOutputs(t)},
		testFaviconCommitError:    fmt.Errorf("injected lost commit acknowledgement"),
		testFaviconReconcileError: fmt.Errorf("injected reconciliation read failure"),
	}
	completion := &multipartCompletion{
		session: model.UploadSession{
			UploadID: uploadID, FileID: fileID, FileName: "favicon.png",
			FileSize: int64(len(source)), Status: model.UploadSessionStatusFinalizing,
		},
		uploadType:   managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON,
		verifiedMime: "image/png",
		objectKey:    "media/" + fileID + ".png",
	}

	err := service.attachOrProjectMultipartCompletion(t.Context(), completion)
	require.Error(t, err)
	require.ErrorIs(t, err, errFaviconBundleCommitUncertain)
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, uploadID))
	var sourceCount int64
	require.NoError(t, db.Model(&model.File{}).Where("id = ? AND delete_requested_at IS NULL", fileID).Count(&sourceCount).Error)
	require.EqualValues(t, 1, sourceCount)
	require.Empty(t, store.deletedKeys())
	set, err := favicon.LoadSet(t.Context(), db, service.cdnDomain, fileID)
	require.NoError(t, err)
	require.NotNil(t, set)
	readyAssetID := set.GetIconPng_32().GetAssetId()
	writtenBeforeRetry := store.writtenKeys()

	service.testFaviconCommitError = nil
	service.testFaviconReconcileError = nil
	require.NoError(t, service.attachOrProjectMultipartCompletion(t.Context(), completion))
	require.NotNil(t, completion.promotedAsset)
	require.Equal(t, readyAssetID, completion.promotedAsset.GetAssetId())
	require.Equal(t, writtenBeforeRetry, store.writtenKeys(), "retry must reuse the committed bundle")
	require.Equal(t, model.UploadSessionStatusFinalizing, loadMultipartCompletionStatus(t, db, uploadID))
	require.Empty(t, store.deletedKeys())
}

func createMultipartPromotionRetrySessionTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE upload_session (
		upload_id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		status TEXT NOT NULL,
		last_activity_at DATETIME,
		updated_at DATETIME
	)`).Error)
}

func insertMultipartPromotionRetrySession(t *testing.T, db *gorm.DB, uploadID string, fileID string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"INSERT INTO upload_session (upload_id, file_id, status, last_activity_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		uploadID,
		fileID,
		model.UploadSessionStatusFinalizing,
		now,
		now,
	).Error)
}

func newMultipartCompletionRetryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:multipart-completion-retry-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session (
		upload_id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		status TEXT NOT NULL,
		last_activity_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE upload_part (
		upload_id TEXT NOT NULL,
		part_number INTEGER NOT NULL,
		etag TEXT NOT NULL,
		size INTEGER NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (upload_id, part_number)
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE file (id TEXT PRIMARY KEY)`).Error)
	return db
}

func insertFinalizingCompletion(t *testing.T, db *gorm.DB, fileSize int64) *multipartCompletion {
	t.Helper()
	now := time.Now().UTC()
	uploadID := uuid.NewString()
	fileID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO upload_session (upload_id, file_id, status, last_activity_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		uploadID,
		fileID,
		model.UploadSessionStatusFinalizing,
		now,
		now,
	).Error)
	return &multipartCompletion{
		session: model.UploadSession{
			UploadID: uploadID,
			FileID:   fileID,
			FileSize: fileSize,
			Status:   model.UploadSessionStatusFinalizing,
		},
		objectKey: "media/" + fileID + ".wav",
	}
}

func loadMultipartCompletionStatus(t *testing.T, db *gorm.DB, uploadID string) model.UploadSessionStatus {
	t.Helper()
	var status model.UploadSessionStatus
	require.NoError(t, db.Table("upload_session").Select("status").Where("upload_id = ?", uploadID).Scan(&status).Error)
	return status
}
