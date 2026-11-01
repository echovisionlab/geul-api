package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const fileIngestProgressPublishInterval = 250 * time.Millisecond

type fileIngestEventEmitter struct {
	ctx            context.Context
	publisher      AsyncPublisher
	nextSequence   fileIngestSequenceAllocator
	source         managev1.FileIngestSource
	mediaKind      managev1.FileIngestMediaKind
	entityType     managev1.TranscodeEntityType
	entityID       string
	correlationID  string
	fileID         string
	target         *domain.ObjectTarget
	uploadID       string
	uploadType     string
	slotID         string
	attemptID      string
	expectedFileID *string
	totalBytes     int64
	sequence       int64
	lastProgress   int32
	lastEvent      string
	lastPublished  time.Time
}

type fileIngestSequenceAllocator func(ctx context.Context, uploadID string) (int64, error)

func newFileIngestEventEmitter(
	ctx context.Context,
	publisher AsyncPublisher,
	source managev1.FileIngestSource,
	entityType managev1.TranscodeEntityType,
	entityID string,
	correlationID string,
	fileID string,
	totalBytes int64,
) *fileIngestEventEmitter {
	if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED && fileID != "" {
		entityType = managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE
		entityID = fileID
	}
	if publisher == nil || entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED || entityID == "" || fileID == "" {
		return nil
	}

	return &fileIngestEventEmitter{
		ctx:           ctx,
		publisher:     publisher,
		source:        source,
		entityType:    entityType,
		entityID:      entityID,
		correlationID: correlationID,
		fileID:        fileID,
		totalBytes:    totalBytes,
		lastProgress:  -1,
	}
}

func (e *fileIngestEventEmitter) setSequenceAllocator(nextSequence fileIngestSequenceAllocator) {
	if e == nil {
		return
	}
	e.nextSequence = nextSequence
}

func (e *fileIngestEventEmitter) setTarget(target *domain.ObjectTarget) {
	if e == nil {
		return
	}
	e.target = target
}

func (e *fileIngestEventEmitter) setUploadIdentity(session domain.UploadSession) {
	if e == nil {
		return
	}
	e.uploadID = session.UploadID
	e.uploadType = session.UploadType
	e.mediaKind = fileIngestMediaKindFromUploadTypeString(session.UploadType)
	if session.SlotID != nil {
		e.slotID = *session.SlotID
	}
	if session.AttemptID != nil {
		e.attemptID = *session.AttemptID
	}
}

func (e *fileIngestEventEmitter) setRequestIdentity(uploadType managev1.UploadType, slotID string, attemptID string) {
	if e == nil {
		return
	}
	if uploadType != managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED {
		e.uploadType = uploadType.String()
		e.mediaKind = fileIngestMediaKindFromUploadType(uploadType)
	}
	e.slotID = slotID
	e.attemptID = attemptID
}

func (e *fileIngestEventEmitter) setProjectionIdentity(
	projection fileIngestProjectionIdentity,
) error {
	if e == nil {
		return nil
	}
	e.expectedFileID = projection.expectedCurrentFileID
	return nil
}

func fileIngestMediaKindFromUploadTypeString(uploadType string) managev1.FileIngestMediaKind {
	if value, ok := managev1.UploadType_value[uploadType]; ok {
		return fileIngestMediaKindFromUploadType(managev1.UploadType(value))
	}
	return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_OTHER
}

func fileIngestMediaKindFromUploadType(uploadType managev1.UploadType) managev1.FileIngestMediaKind {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_VIDEO
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH
	case managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO
	default:
		return managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_OTHER
	}
}

func optionalIngestString(value string) *string {
	if value == "" {
		return nil
	}
	next := value
	return &next
}

func fileIngestFailureReason(errText string) managev1.FileIngestFailureReason {
	lower := strings.ToLower(errText)
	switch {
	case strings.Contains(lower, "abort") || strings.Contains(lower, "cancel"):
		return managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_ABORTED
	case strings.Contains(lower, "mime") ||
		strings.Contains(lower, "unsupported") ||
		strings.Contains(lower, "invalid") ||
		strings.Contains(lower, "exceed") ||
		strings.Contains(lower, "below minimum") ||
		strings.Contains(lower, "not allowed"):
		return managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_REJECTED
	case strings.Contains(lower, "s3") ||
		strings.Contains(lower, "multipart") ||
		strings.Contains(lower, "storage"):
		return managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_STORAGE_FAILED
	default:
		return managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_INTERNAL
	}
}

func (e *fileIngestEventEmitter) progress(progress int32, bytesCompleted *int64) *domain.IngestProgress {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	next := &domain.IngestProgress{
		Percentage: progress,
	}
	if bytesCompleted != nil {
		next.BytesCompleted = bytesCompleted
	}
	if e.totalBytes > 0 {
		total := e.totalBytes
		next.BytesTotal = &total
	}
	return next
}

func (e *fileIngestEventEmitter) nextEventFields() (string, *domain.IngestIdentity, int64, int64, bool) {
	if e.nextSequence != nil && e.uploadID != "" {
		sequence, err := e.nextSequence(e.ctx, e.uploadID)
		if err != nil {
			slog.ErrorContext(e.ctx, "Failed to allocate file ingest event sequence", failurecause.Attr(err), "uploadId", e.uploadID, "fileId", e.fileID, "entityType", e.entityType.String(), "entityId", e.entityID, "correlationId", e.correlationID, "attemptId", e.attemptID, "slotId", e.slotID)
			return "", nil, 0, 0, false
		}
		e.sequence = sequence
	} else {
		e.sequence += 1
	}
	return e.correlationID,
		&domain.IngestIdentity{
			EntityType:            int32(e.entityType),
			EntityID:              e.entityID,
			FileID:                e.fileID,
			Target:                e.target,
			Source:                int32(e.source),
			MediaKind:             int32(e.mediaKind),
			UploadID:              optionalIngestString(e.uploadID),
			SlotID:                optionalIngestString(e.slotID),
			AttemptID:             optionalIngestString(e.attemptID),
			ExpectedCurrentFileID: e.expectedFileID,
		},
		e.sequence,
		time.Now().UnixMilli(),
		true
}

func (e *fileIngestEventEmitter) publishWithResult(
	event domain.IngestEvent,
	eventName string,
	progress int32,
) error {
	if e == nil {
		return nil
	}

	if err := e.publisher.PublishIngest(e.ctx, event); err != nil {
		slog.ErrorContext(e.ctx, "Failed to publish file ingest lifecycle event",
			failurecause.Attr(err),
			"event", eventName,
			"entityType", e.entityType.String(),
			"entityId", e.entityID,
			"fileId", e.fileID,
			"uploadId", e.uploadID,
			"slotId", e.slotID,
			"attemptId", e.attemptID,
			"correlationId", e.correlationID,
			"progress", progress,
		)
		return err
	}

	e.lastEvent = eventName
	e.lastProgress = progress
	e.lastPublished = time.Now()

	slog.Debug("Published file ingest lifecycle event",
		"event", eventName,
		"entityType", e.entityType.String(),
		"entityId", e.entityID,
		"fileId", e.fileID,
		"uploadId", e.uploadID,
		"slotId", e.slotID,
		"attemptId", e.attemptID,
		"correlationId", e.correlationID,
		"progress", progress,
	)
	return nil
}

func (e *fileIngestEventEmitter) publish(event domain.IngestEvent, eventName string, progress int32) {
	_ = e.publishWithResult(event, eventName, progress)
}

func (e *fileIngestEventEmitter) publishUploading(progress int32, bytesCompleted *int64) {
	if e == nil {
		return
	}
	correlationID, identity, sequence, timestampMs, ok := e.nextEventFields()
	if !ok {
		return
	}
	event := domain.IngestEvent{
		Kind:           domain.IngestUploading,
		CorrelationID:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMS:    timestampMs,
		Progress:       e.progress(progress, bytesCompleted),
	}
	e.publish(event, "uploading", progress)
}

func (e *fileIngestEventEmitter) publishDownloading(progress int32, bytesCompleted *int64) {
	if e == nil {
		return
	}
	correlationID, identity, sequence, timestampMs, ok := e.nextEventFields()
	if !ok {
		return
	}
	event := domain.IngestEvent{
		Kind:           domain.IngestDownloading,
		CorrelationID:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMS:    timestampMs,
		Progress:       e.progress(progress, bytesCompleted),
	}
	e.publish(event, "downloading", progress)
}

func (e *fileIngestEventEmitter) publishFinalized(progress int32, bytesCompleted *int64) {
	if e == nil {
		return
	}
	correlationID, identity, sequence, timestampMs, ok := e.nextEventFields()
	if !ok {
		return
	}
	event := domain.IngestEvent{
		Kind:           domain.IngestFinalized,
		CorrelationID:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMS:    timestampMs,
		Progress:       e.progress(progress, bytesCompleted),
	}
	e.publish(event, "finalized", progress)
}

func (e *fileIngestEventEmitter) publishAttachedConfirmed(
	fileName string,
	mimeType string,
	fileSize int64,
) error {
	if e == nil {
		return fmt.Errorf("file ingest emitter is required")
	}
	if e.entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK ||
		e.mediaKind != managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO {
		return fmt.Errorf("file ingest attached is reserved for Track original audio")
	}
	correlationID, identity, sequence, timestampMs, ok := e.nextEventFields()
	if !ok {
		return fmt.Errorf("failed to allocate file ingest attached event identity")
	}
	event := domain.IngestEvent{
		Kind:           domain.IngestAttached,
		CorrelationID:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMS:    timestampMs,
		FileName:       fileName,
		MimeType:       mimeType,
		FileSize:       fileSize,
	}
	return e.publishWithResult(event, "attached", 100)
}

func (e *fileIngestEventEmitter) publishFailed(errText string, progress int32, bytesCompleted *int64) {
	if e == nil {
		return
	}
	correlationID, identity, sequence, timestampMs, ok := e.nextEventFields()
	if !ok {
		return
	}
	event := domain.IngestEvent{
		Kind:           domain.IngestFailed,
		CorrelationID:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMS:    timestampMs,
		Reason:         int32(fileIngestFailureReason(errText)),
		Error:          errText,
		Progress:       e.progress(progress, bytesCompleted),
	}
	e.publish(event, "failed", progress)
}

func (e *fileIngestEventEmitter) publishDownloadProgress(bytesCompleted int64) {
	if e == nil {
		return
	}

	progress := int32(0)
	if e.totalBytes > 0 {
		progress = min(int32((bytesCompleted*100)/e.totalBytes), 100)
	}

	now := time.Now()
	if e.lastEvent == "downloading" &&
		progress == e.lastProgress &&
		now.Sub(e.lastPublished) < fileIngestProgressPublishInterval {
		return
	}

	completed := bytesCompleted
	e.publishDownloading(progress, &completed)
}
