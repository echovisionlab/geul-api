package worker

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleCleanupIncompleteUploads(ctx context.Context) error {
	// Clean up upload_session records with no recent activity for 7 days. Object
	// storage is cleaned explicitly; a bucket lifecycle rule is defense in depth,
	// not the authority for deleting the corresponding database session.
	now := time.Now()
	cutoff := now.Add(-filemedia.IncompleteUploadRetention)

	expiredUploads, err := h.findExpiredUploadSessions(ctx, cutoff)
	if err != nil {
		return err
	}

	var cleanupErr error
	var cleaned int64
	for _, session := range expiredUploads {
		removed, err := h.cleanupExpiredUploadSession(ctx, session, cutoff, now)
		if err != nil {
			cleanupErr = stderrors.Join(cleanupErr, err)
			continue
		}
		if removed {
			cleaned++
		}
	}

	if cleaned > 0 {
		slog.Info("Cleaned up old upload sessions", "count", cleaned)
	}
	return cleanupErr
}

type expiredUploadSession struct {
	UploadType            string  `gorm:"column:upload_type"`
	UploadID              string  `gorm:"column:upload_id"`
	FileID                string  `gorm:"column:file_id"`
	RequestedMime         string  `gorm:"column:requested_mime"`
	EntityID              string  `gorm:"column:entity_id"`
	EntityType            *string `gorm:"column:entity_type"`
	SlotID                *string `gorm:"column:slot_id"`
	AttemptID             *string `gorm:"column:attempt_id"`
	ExpectedCurrentFileID *string `gorm:"column:expected_current_file_id"`
	Status                string  `gorm:"column:status"`
}

func (h *Handlers) findExpiredUploadSessions(ctx context.Context, cutoff time.Time) ([]expiredUploadSession, error) {
	var sessions []expiredUploadSession
	err := h.db.WithContext(ctx).
		Table("upload_session").
		Select(
			"upload_type", "upload_id", "file_id", "requested_mime", "entity_id", "entity_type",
			"slot_id", "attempt_id",
			"expected_current_file_id", "status",
		).
		Where("status IN ? AND last_activity_at < ?", expiredUploadCleanupStatuses(), cutoff).
		Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (h *Handlers) cleanupExpiredUploadSession(
	ctx context.Context,
	session expiredUploadSession,
	cutoff time.Time,
	expiredAt time.Time,
) (bool, error) {
	if strings.TrimSpace(session.UploadID) == "" || strings.TrimSpace(session.FileID) == "" {
		return false, fmt.Errorf("expired upload session is missing its storage identity")
	}
	if h.s3Client == nil || h.config == nil || strings.TrimSpace(h.config.S3Bucket) == "" {
		return false, fmt.Errorf("expired upload cleanup requires object storage configuration")
	}

	claim := h.db.WithContext(ctx).
		Table("upload_session").
		Where(
			"upload_id = ? AND status IN ? AND last_activity_at < ?",
			session.UploadID,
			expiredUploadCleanupStatuses(),
			cutoff,
		).
		Update("status", model.UploadSessionStatusAborted)
	if claim.Error != nil {
		return false, fmt.Errorf("claim expired upload session %s: %w", session.UploadID, claim.Error)
	}
	if claim.RowsAffected == 0 {
		return false, nil
	}

	extension := model.GetExtensionFromMime(session.RequestedMime)
	objectKey, err := mediaauth.MediaObjectKey(session.FileID, extension)
	if err != nil {
		return false, fmt.Errorf("build expired upload target %s: %w", session.UploadID, err)
	}
	if _, err := h.s3Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(h.config.S3Bucket),
		Key:      aws.String(objectKey),
		UploadId: aws.String(session.UploadID),
	}); err != nil && !filemedia.IsMissingMultipartUploadAbortError(err) {
		return false, fmt.Errorf("abort expired multipart upload %s: %w", session.UploadID, err)
	}

	if isExpiredFileIngestLifecycleUploadType(session.UploadType) {
		if _, err := h.publishExpiredFileIngest(ctx, session, expiredAt); err != nil {
			// This event is a realtime UI projection. Storage cleanup remains
			// authoritative and must not be retained until the publisher recovers.
			slog.Warn(
				"Failed to publish expired file ingest lifecycle before cleanup",
				"error", err,
				"uploadId", session.UploadID,
				"fileId", session.FileID,
			)
		}
	}

	result := h.db.WithContext(ctx).
		Table("upload_session").
		Where("upload_id = ? AND status = ? AND last_activity_at < ?", session.UploadID, model.UploadSessionStatusAborted, cutoff).
		Delete(nil)
	if result.Error != nil {
		return false, fmt.Errorf("delete expired upload session %s: %w", session.UploadID, result.Error)
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("expired upload session %s changed before deletion", session.UploadID)
	}
	return true, nil
}

func expiredUploadCleanupStatuses() []model.UploadSessionStatus {
	return []model.UploadSessionStatus{
		model.UploadSessionStatusInitiated,
		model.UploadSessionStatusUploading,
		model.UploadSessionStatusFailed,
		model.UploadSessionStatusAborted,
	}
}

func isExpiredFileIngestLifecycleUploadType(uploadType string) bool {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO.String(),
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(),
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String():
		return true
	default:
		return false
	}
}

func isExpiredFileIngestLifecyclePublishable(session expiredUploadSession) bool {
	entityType := expiredUploadEntityType(session)
	entityID := expiredUploadEntityID(session, entityType)
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED ||
		entityID == "" ||
		session.FileID == "" ||
		session.RequestedMime == "" {
		return false
	}
	_, err := expiredUploadProjectionIdentity(session, entityType)
	return err == nil
}

func (h *Handlers) publishExpiredFileIngest(ctx context.Context, session expiredUploadSession, expiredAt time.Time) (bool, error) {
	entityType := expiredUploadEntityType(session)
	entityID := expiredUploadEntityID(session, entityType)
	if !isExpiredFileIngestLifecyclePublishable(session) {
		slog.Warn(
			"Skipping expired file ingest lifecycle without required identity",
			"uploadType", session.UploadType,
			"entityType", entityType.String(),
			"entityId", entityID,
			"fileId", session.FileID,
			"uploadId", session.UploadID,
			"slotId", uploadStringValue(session.SlotID),
			"attemptId", uploadStringValue(session.AttemptID),
		)
		return false, nil
	}

	event, err := expiredFileIngestFailedEvent(session, entityType, expiredAt)
	if err != nil {
		return false, fmt.Errorf("restore expired file ingest identity: %w", err)
	}
	if h.fileIngest == nil {
		return false, fmt.Errorf("file ingest lifecycle publisher is not configured")
	}
	if err := h.fileIngest.PublishFileIngest(ctx, event); err != nil {
		slog.Warn(
			"Failed to publish expired file ingest lifecycle",
			"error", err,
			"uploadType", session.UploadType,
			"entityType", entityType.String(),
			"entityId", entityID,
			"fileId", session.FileID,
			"uploadId", session.UploadID,
		)
		return false, err
	}
	return true, nil
}

func expiredFileIngestFailedEvent(
	session expiredUploadSession,
	entityType managev1.TranscodeEntityType,
	expiredAt time.Time,
) (*managev1.FileIngestFailedEvent, error) {
	expectedCurrentFileID, err := expiredUploadProjectionIdentity(session, entityType)
	if err != nil {
		return nil, err
	}
	extension := model.GetExtensionFromMime(session.RequestedMime)
	objectKey, err := mediaauth.MediaObjectKey(session.FileID, extension)
	if err != nil {
		return nil, fmt.Errorf("build expired upload media target: %w", err)
	}
	return &managev1.FileIngestFailedEvent{
		CorrelationId:  session.UploadID,
		SequenceNumber: 1,
		TimestampMs:    expiredAt.UnixMilli(),
		Identity: &managev1.FileIngestIdentity{
			EntityType: entityType,
			EntityId:   expiredUploadEntityID(session, entityType),
			FileId:     session.FileID,
			Target: &commonv1.MediaObjectTarget{
				FileId:    session.FileID,
				ObjectKey: objectKey,
				Extension: extension,
				MimeType:  session.RequestedMime,
			},
			Source:                managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
			MediaKind:             expiredUploadMediaKind(session.UploadType),
			UploadId:              optionalUploadString(session.UploadID),
			SlotId:                optionalUploadString(uploadStringValue(session.SlotID)),
			AttemptId:             optionalUploadString(uploadStringValue(session.AttemptID)),
			ExpectedCurrentFileId: expectedCurrentFileID,
		},
		Reason: managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_EXPIRED,
		Error:  "upload session expired before file verification",
		Progress: &managev1.FileIngestProgress{
			Percentage: 0,
		},
	}, nil
}

func expiredUploadProjectionIdentity(
	session expiredUploadSession,
	entityType managev1.TranscodeEntityType,
) (*string, error) {
	var expectedCurrentFileID *string
	if session.ExpectedCurrentFileID != nil {
		value := strings.TrimSpace(*session.ExpectedCurrentFileID)
		if _, err := uuid.Parse(value); err != nil {
			return nil, fmt.Errorf("expected current file ID is invalid")
		}
		expectedCurrentFileID = &value
	}

	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		return expectedCurrentFileID, nil
	}

	slotID := strings.TrimSpace(uploadStringValue(session.SlotID))
	if slotID != "" || expectedCurrentFileID != nil {
		return nil, fmt.Errorf("editor file upload contains a document attachment target")
	}
	return nil, nil
}

func uploadStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalUploadString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func expiredUploadEntityType(session expiredUploadSession) managev1.TranscodeEntityType {
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
	}
	if isExpiredFileIngestLifecycleUploadType(session.UploadType) {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	}
	return parseTranscodeEntityType(session.EntityType)
}

func expiredUploadEntityID(
	session expiredUploadSession,
	entityType managev1.TranscodeEntityType,
) string {
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE {
		return session.FileID
	}
	return session.EntityID
}

func expiredUploadMediaKind(uploadType string) managev1.FileIngestMediaKind {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_VIDEO
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH
	case managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String():
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO
	default:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_OTHER
	}
}

func parseTranscodeEntityType(value *string) managev1.TranscodeEntityType {
	if value == nil || *value == "" {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	}
	if enumValue, ok := managev1.TranscodeEntityType_value[*value]; ok {
		return managev1.TranscodeEntityType(enumValue)
	}
	return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
}
