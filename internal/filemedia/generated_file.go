package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

// GeneratedGeneralFileInput is a bounded server-generated File. It follows
// the existing verified File record, ingest binding, Audit, authorization and
// media object-key contracts; it is not a second artifact store.
type GeneratedGeneralFileInput struct {
	FileName  string
	Extension string
	MimeType  string
	Body      []byte
}

type VerifiedFileBody struct {
	FileID    string
	Extension string
	MimeType  string
	Body      []byte
}

// CreateGeneratedGeneralFile stores one request-actor-owned immutable File and
// returns its existing signed download delivery. The caller must validate its
// format-specific body before this boundary.
func (s *FileService) CreateGeneratedGeneralFile(
	ctx context.Context,
	input GeneratedGeneralFileInput,
) (*commonv1.ExpiringMediaRef, error) {
	if s == nil || s.db == nil || s.s3Client == nil || strings.TrimSpace(s.s3Bucket) == "" {
		return nil, fmt.Errorf("generated File storage runtime is required")
	}
	extension := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(input.Extension), "."))
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(input.MimeType, ";")[0]))
	if strings.TrimSpace(input.FileName) == "" || extension == "" || mimeType == "" || len(input.Body) == 0 {
		return nil, errs.InvalidArgument("artifact", "file name, extension, MIME type, and body are required")
	}
	if len(extension) > 16 {
		return nil, errs.InvalidArgument("artifact.extension", "extension is too long")
	}

	fileID := uuid.NewString()
	fileName := storedFileBasename(input.FileName, fileID, extension)
	objectKey, err := mediaauth.MediaObjectKey(fileID, extension)
	if err != nil {
		return nil, errs.Internal(err)
	}
	download, err := buildExpiringMediaFileRef(
		s.mediaDomain, s.mediaSecret, fileID, extension, mimeType, &fileName,
		mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	digest := sha256.Sum256(input.Body)
	if _, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(objectKey),
		Body: bytes.NewReader(input.Body), ContentLength: aws.Int64(int64(len(input.Body))),
		ContentType: aws.String(mimeType),
	}); err != nil {
		return nil, errs.Internal(fmt.Errorf("store generated File object: %w", err))
	}

	file := structured.Fields{
		"id": fileID, "file_name": fileName, "mime_type": mimeType,
		"file_size": int64(len(input.Body)), "extension": extension, "sha256": digest[:],
	}
	createErr := s.createVerifiedFileIngestRecord(
		ctx, file,
		managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		"",
		requestFileIngestAuthority{},
	)
	if createErr == nil {
		return download, nil
	}

	// A database commit error can be uncertain after the SpiceDB write. Re-read
	// the authoritative File row before deciding whether the staged object is
	// orphaned; never delete an object for a transaction that did commit.
	exists, existsErr := s.completedFileRecordExists(context.WithoutCancel(ctx), fileID)
	if existsErr == nil && exists {
		return download, nil
	}
	if existsErr != nil {
		return nil, errs.Internal(errors.Join(createErr, fmt.Errorf("verify generated File commit: %w", existsErr)))
	}
	cleanupErr := s.deleteGeneratedFileObject(context.WithoutCancel(ctx), objectKey)
	return nil, errs.Internal(errors.Join(createErr, cleanupErr))
}

func (s *FileService) deleteGeneratedFileObject(ctx context.Context, objectKey string) error {
	_, err := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("delete uncommitted generated File object: %w", err)
	}
	return nil
}

// ReadVerifiedFileBody authorizes one existing completed File through the
// normal manage-delivery boundary, then reads and verifies its canonical media
// object with a caller-supplied hard size bound.
func (s *FileService) ReadVerifiedFileBody(
	ctx context.Context,
	fileID string,
	maximumBytes int64,
) (_ VerifiedFileBody, retErr error) {
	if s == nil || s.db == nil || s.s3Client == nil || strings.TrimSpace(s.s3Bucket) == "" {
		return VerifiedFileBody{}, fmt.Errorf("verified File storage runtime is required")
	}
	if maximumBytes <= 0 {
		return VerifiedFileBody{}, errs.InvalidArgument("maximum_bytes", "positive size limit is required")
	}
	response, err := s.GetMediaDelivery(ctx, connect.NewRequest(
		&managev1.GetMediaDeliveryRequest{FileId: strings.TrimSpace(fileID)},
	))
	if err != nil {
		return VerifiedFileBody{}, err
	}
	if response == nil || response.Msg == nil || response.Msg.Delivery == nil {
		return VerifiedFileBody{}, errs.NotFound("file", fileID)
	}
	delivery := response.Msg.Delivery
	if delivery.FileSize <= 0 || delivery.FileSize > maximumBytes {
		return VerifiedFileBody{}, errs.InvalidArgument("file_id", "File is empty or exceeds the size limit")
	}

	var stored model.File
	result := s.db.WithContext(ctx).
		Select("id", "extension", "mime_type", "file_size", "sha256", "delete_requested_at").
		Where("id = ? AND delete_requested_at IS NULL", delivery.FileId).
		Take(&stored)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return VerifiedFileBody{}, errs.NotFound("file", fileID)
	}
	if result.Error != nil {
		return VerifiedFileBody{}, errs.Internal(fmt.Errorf("load verified File metadata: %w", result.Error))
	}
	if stored.ID != delivery.FileId || stored.Extension != delivery.Extension ||
		stored.MimeType != delivery.MimeType || stored.FileSize != delivery.FileSize {
		return VerifiedFileBody{}, errs.FailedPrecondition("verified File delivery metadata changed")
	}
	objectKey, err := mediaauth.MediaObjectKey(stored.ID, stored.Extension)
	if err != nil {
		return VerifiedFileBody{}, errs.Internal(err)
	}
	object, err := s.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3Bucket), Key: aws.String(objectKey),
	})
	if err != nil {
		return VerifiedFileBody{}, errs.Internal(fmt.Errorf("read verified File object: %w", err))
	}
	defer func() {
		if closeErr := object.Body.Close(); closeErr != nil && retErr == nil {
			retErr = errs.Internal(fmt.Errorf("close verified File object: %w", closeErr))
		}
	}()
	body, err := io.ReadAll(io.LimitReader(object.Body, maximumBytes+1))
	if err != nil {
		return VerifiedFileBody{}, errs.Internal(fmt.Errorf("read verified File body: %w", err))
	}
	if int64(len(body)) != stored.FileSize || int64(len(body)) > maximumBytes {
		return VerifiedFileBody{}, errs.FailedPrecondition("verified File object size does not match metadata")
	}
	if len(stored.SHA256) != sha256.Size {
		return VerifiedFileBody{}, errs.FailedPrecondition("verified File checksum is unavailable")
	}
	digest := sha256.Sum256(body)
	if !bytes.Equal(stored.SHA256, digest[:]) {
		return VerifiedFileBody{}, errs.FailedPrecondition("verified File checksum does not match the object")
	}
	return VerifiedFileBody{
		FileID: stored.ID, Extension: stored.Extension, MimeType: stored.MimeType, Body: body,
	}, nil
}
