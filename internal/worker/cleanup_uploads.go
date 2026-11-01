package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
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

	var cleaned, failed int64
	for _, session := range expiredUploads {
		removed, err := h.cleanupExpiredUploadSession(ctx, session, cutoff, now)
		if err != nil {
			// The next run retries the session; this is the only record of why it
			// was left behind.
			failed++
			slog.ErrorContext(ctx, "Expired upload session cleanup failed", append(failurecause.Args(err), "upload_id", session.UploadID, "file_id", session.FileID, "upload_type", session.UploadType)...)
			continue
		}
		if removed {
			cleaned++
		}
	}

	if cleaned > 0 {
		slog.InfoContext(ctx, "Cleaned up old upload sessions", "count", cleaned)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d expired upload sessions were not cleaned up", failed, len(expiredUploads))
	}
	return nil
}

func (h *Handlers) findExpiredUploadSessions(ctx context.Context, cutoff time.Time) ([]filemediaadapter.ExpiredUploadSession, error) {
	return filemediaadapter.ExpiredUploadSessions(ctx, h.db, expiredUploadCleanupStatuses(), cutoff)
}

func (h *Handlers) cleanupExpiredUploadSession(
	ctx context.Context,
	session filemediaadapter.ExpiredUploadSession,
	cutoff time.Time,
	expiredAt time.Time,
) (bool, error) {
	if strings.TrimSpace(session.UploadID) == "" || strings.TrimSpace(session.FileID) == "" {
		return false, fmt.Errorf("expired upload session is missing its storage identity")
	}
	if h.s3Client == nil || h.config == nil || strings.TrimSpace(h.config.S3Bucket) == "" {
		return false, fmt.Errorf("expired upload cleanup requires object storage configuration")
	}

	claimed, err := filemediaadapter.ClaimExpiredUploadSession(ctx, h.db, session.UploadID, expiredUploadCleanupStatuses(), cutoff)
	if err != nil {
		return false, fmt.Errorf("claim expired upload session %s: %w", session.UploadID, err)
	}
	if !claimed {
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

	if session.ClientMediaBundleID != nil && session.ClientMediaManifest != nil {
		if err := filemedia.CleanupClientMediaStaging(ctx, h.s3Client, h.config.S3Bucket, *session.ClientMediaBundleID, *session.ClientMediaManifest); err != nil {
			return false, err
		}
	}
	if isExpiredFileIngestLifecycleUploadType(session.UploadType) {
		if _, err := h.publishExpiredFileIngest(ctx, session, expiredAt); err != nil {
			// This event is a realtime UI projection. Storage cleanup remains
			// authoritative and must not be retained until the publisher recovers.
			slog.WarnContext(ctx, "Failed to publish expired file ingest lifecycle before cleanup", append(failurecause.Args(err), "upload_id", session.UploadID, "file_id", session.FileID, "upload_type", session.UploadType)...)
		}
	}

	deleted, err := filemediaadapter.DeleteAbortedUploadSession(ctx, h.db, session.UploadID, cutoff)
	if err != nil {
		return false, fmt.Errorf("delete expired upload session %s: %w", session.UploadID, err)
	}
	if deleted != 1 {
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

// expiredFileIngestLifecycleIdentityError reports why an expired session
// cannot identify the file ingest lifecycle it would end, or nil.
func expiredFileIngestLifecycleIdentityError(session filemediaadapter.ExpiredUploadSession) error {
	entityType := expiredUploadEntityType(session)
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED ||
		expiredUploadEntityID(session, entityType) == "" ||
		session.FileID == "" ||
		session.RequestedMime == "" {
		return fmt.Errorf("entity, file or requested MIME type is missing")
	}
	_, err := expiredUploadProjectionIdentity(session, entityType)
	return err
}

func (h *Handlers) publishExpiredFileIngest(ctx context.Context, session filemediaadapter.ExpiredUploadSession, expiredAt time.Time) (bool, error) {
	entityType := expiredUploadEntityType(session)
	entityID := expiredUploadEntityID(session, entityType)
	if err := expiredFileIngestLifecycleIdentityError(session); err != nil {
		slog.WarnContext(ctx, "Skipping expired file ingest lifecycle without required identity",
			"failure_cause", failurecause.Message(err),
			"upload_type", session.UploadType,
			"entity_type", entityType.String(),
			"entity_id", entityID,
			"file_id", session.FileID,
			"upload_id", session.UploadID,
			"slot_id", uploadStringValue(session.SlotID),
			"attempt_id", uploadStringValue(session.AttemptID),
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
		return false, failurecause.WithAttrs(err, slog.String("entity_type", entityType.String()), slog.String("entity_id", entityID))
	}
	return true, nil
}

func expiredFileIngestFailedEvent(
	session filemediaadapter.ExpiredUploadSession,
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
	session filemediaadapter.ExpiredUploadSession,
	entityType managev1.TranscodeEntityType,
) (*string, error) {
	var expectedCurrentFileID *string
	if session.ExpectedCurrentFileID != nil {
		value := strings.TrimSpace(*session.ExpectedCurrentFileID)
		if _, err := uuid.Parse(value); err != nil {
			return nil, fmt.Errorf("expected current file ID is invalid: %w", err)
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

func expiredUploadEntityType(session filemediaadapter.ExpiredUploadSession) managev1.TranscodeEntityType {
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
	}
	if isExpiredFileIngestLifecycleUploadType(session.UploadType) {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
	}
	return parseTranscodeEntityType(session.EntityType)
}

func expiredUploadEntityID(
	session filemediaadapter.ExpiredUploadSession,
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
