package workerpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediamime"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/worker"
)

// expiredUploadStatuses are the unfinished sessions the cleanup claims; a
// finalizing session is left to its finalizer.
var expiredUploadStatuses = []model.UploadSessionStatus{
	model.UploadSessionStatusInitiated,
	model.UploadSessionStatusUploading,
	model.UploadSessionStatusFailed,
	model.UploadSessionStatusAborted,
}

type uploadSessions struct{ db *gorm.DB }

func (u uploadSessions) ExpiredUploads(ctx context.Context, cutoff time.Time) ([]worker.ExpiredUpload, error) {
	sessions, err := filemediaadapter.ExpiredUploadSessions(ctx, u.db, expiredUploadStatuses, cutoff)
	if err != nil {
		return nil, err
	}
	expired := make([]worker.ExpiredUpload, 0, len(sessions))
	for _, session := range sessions {
		expired = append(expired, expiredUpload(session))
	}
	return expired, nil
}

func expiredUpload(session filemediaadapter.ExpiredUploadSession) worker.ExpiredUpload {
	return worker.ExpiredUpload{
		UploadID:              session.UploadID,
		FileID:                session.FileID,
		UploadType:            session.UploadType,
		RequestedMime:         session.RequestedMime,
		Extension:             mediamime.GetExtensionFromMime(session.RequestedMime),
		EntityID:              session.EntityID,
		EntityType:            session.EntityType,
		SlotID:                session.SlotID,
		AttemptID:             session.AttemptID,
		ExpectedCurrentFileID: session.ExpectedCurrentFileID,
		ClientMediaBundleID:   session.ClientMediaBundleID,
		ClientMediaManifest:   session.ClientMediaManifest,
	}
}

func (u uploadSessions) ClaimExpiredUpload(ctx context.Context, uploadID string, cutoff time.Time) (bool, error) {
	return filemediaadapter.ClaimExpiredUploadSession(ctx, u.db, uploadID, expiredUploadStatuses, cutoff)
}

func (u uploadSessions) DeleteAbortedUpload(ctx context.Context, uploadID string, cutoff time.Time) (int64, error) {
	return filemediaadapter.DeleteAbortedUploadSession(ctx, u.db, uploadID, cutoff)
}

// uploadStorage is the media bucket.
type uploadStorage struct {
	client *s3.Client
	bucket string
}

func (s uploadStorage) AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(uploadID),
	})
	return err
}

func (s uploadStorage) CleanupClientMediaStaging(ctx context.Context, bundleID, manifest string) error {
	return filemedia.CleanupClientMediaStaging(ctx, s.client, s.bucket, bundleID, manifest)
}
