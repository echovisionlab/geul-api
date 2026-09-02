package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filemediaapplication "github.com/echovisionlab/geul-api/internal/filemedia/application"
	transcodeapplication "github.com/echovisionlab/geul-api/internal/transcode/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Publisher is the runtime output boundary shared by FileMedia and Transcode
// queue consumers.
type Publisher interface {
	filemedia.FileDeletePublisher
	transcodeapplication.Publisher
}

type Runtime struct {
	db        *gorm.DB
	publisher Publisher
	transcode *transcodeapplication.Application
	spiceDB   *auth.SpiceDBClient
	s3Client  *s3.Client
	bucket    string
	fileAuth  filemediaapplication.AuthorizationDeletion
}

func New(
	db *gorm.DB,
	publisher Publisher,
	jobs transcodeapplication.JobTracker,
	spiceDB *auth.SpiceDBClient,
	s3Client *s3.Client,
	bucket string,
	fileAuth filemediaapplication.AuthorizationDeletion,
) *Runtime {
	if db == nil {
		panic("filemedia runtime: db is required")
	}
	return &Runtime{
		db:        db,
		publisher: publisher,
		transcode: transcodeapplication.New(db, publisher, jobs),
		spiceDB:   spiceDB,
		s3Client:  s3Client,
		bucket:    strings.TrimSpace(bucket),
		fileAuth:  fileAuth,
	}
}

func (r *Runtime) HandleFileDelete(
	ctx context.Context,
	event *managev1.FileDeleteEvent,
) error {
	if err := filemediaapplication.ValidateDeleteEvent(event); err != nil {
		return fmt.Errorf("%w: %v", filemediaapplication.ErrInvalidFileDeleteTarget, err)
	}
	deletion := filemediaapplication.NewDeletion(r.db, r.fileAuth)
	exists, err := deletion.ValidatePending(ctx, event)
	if err != nil || !exists {
		return err
	}
	if err := r.deleteFileObjects(ctx, event); err != nil {
		return err
	}
	return deletion.Finalize(ctx, event)
}

func (r *Runtime) deleteFileObjects(ctx context.Context, event *managev1.FileDeleteEvent) error {
	if r.s3Client == nil || r.bucket == "" {
		return errors.New("file storage runtime is required")
	}
	original := event.GetOriginal()
	derivativeCount := len(event.GetAssets()) + len(event.GetGenerations())
	slog.Info("Deleting file objects from storage",
		"fileId", event.GetFileId(),
		"bucket", r.bucket,
		"derivativeCount", derivativeCount,
	)

	if original.GetObjectKey() != "" {
		if _, err := r.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(r.bucket),
			Key:    aws.String(original.GetObjectKey()),
		}); err != nil {
			return errors.New("failed to delete original storage object")
		}
	}

	for _, asset := range event.GetAssets() {
		if asset.GetObjectKey() == "" {
			continue
		}
		if _, err := r.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(r.bucket),
			Key:    aws.String(asset.GetObjectKey()),
		}); err != nil {
			return errors.New("failed to delete asset storage object")
		}
	}

	for _, generation := range event.GetGenerations() {
		if err := r.deleteS3Prefix(ctx, generation.GetObjectPrefix()); err != nil {
			return errors.New("failed to delete media generation storage objects")
		}

	}

	slog.Info("File objects deleted successfully", "fileId", event.GetFileId(), "derivativesDeleted", derivativeCount)
	return nil
}

func (r *Runtime) deleteS3Prefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return nil
	}
	paginator := s3.NewListObjectsV2Paginator(r.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list objects: %w", err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			objects = append(objects, s3types.ObjectIdentifier{Key: object.Key})
		}
		if _, err := r.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(r.bucket),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return fmt.Errorf("failed to batch delete objects: %w", err)
		}
	}
	return nil
}

func (r *Runtime) HandleTranscodeProgress(ctx context.Context, body []byte) error {
	return r.transcode.HandleTranscodeProgress(ctx, body)
}

func (r *Runtime) HandleTranscodeComplete(ctx context.Context, body []byte) error {
	return r.transcode.HandleTranscodeComplete(ctx, body)
}

func (r *Runtime) HandleWaveformProgress(ctx context.Context, body []byte) error {
	return r.transcode.HandleWaveformProgress(ctx, body)
}

func (r *Runtime) HandleWaveformComplete(ctx context.Context, body []byte) error {
	return r.transcode.HandleWaveformComplete(ctx, body)
}

func (r *Runtime) HandleWaveformFail(ctx context.Context, body []byte) error {
	return r.transcode.HandleWaveformFail(ctx, body)
}

func (r *Runtime) HandleMeshOptimizationProgress(
	ctx context.Context,
	body []byte,
) error {
	var event managev1.MeshOptimizationProgressEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("invalid mesh optimization progress event: %w", err)
	}
	_, err := r.meshOptimizationService().HandleProgress(ctx, &event)
	return err
}

func (r *Runtime) HandleMeshOptimizationComplete(
	ctx context.Context,
	body []byte,
) error {
	var event managev1.MeshOptimizationCompleteEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("invalid mesh optimization complete event: %w", err)
	}
	_, err := r.meshOptimizationService().HandleComplete(ctx, &event)
	return err
}

func (r *Runtime) HandleMeshOptimizationFail(
	ctx context.Context,
	body []byte,
) error {
	var event managev1.MeshOptimizationFailEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("invalid mesh optimization fail event: %w", err)
	}
	_, err := r.meshOptimizationService().HandleFailed(ctx, &event)
	return err
}

func (r *Runtime) ExpireStaleMeshOptimizationCandidates(ctx context.Context, now time.Time) (int64, error) {
	return r.meshOptimizationService().ExpireStaleUnselectedCandidates(ctx, now)
}

func (r *Runtime) meshOptimizationService() *filemedia.MeshOptimizationService {
	return filemedia.NewMeshOptimizationService(
		r.db,
		nil,
		meshOptimizationFileDeleter{runtime: r},
		r.spiceDB,
	)
}

type meshOptimizationFileDeleter struct{ runtime *Runtime }

func (d meshOptimizationFileDeleter) DeleteFileByID(ctx context.Context, fileID string) error {
	if d.runtime == nil || d.runtime.publisher == nil || fileID == "" {
		return nil
	}
	return filemedia.RequestFileDeletion(ctx, d.runtime.db, d.runtime.publisher, fileID)
}
