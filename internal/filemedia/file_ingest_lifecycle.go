package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
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
	target         *commonv1.MediaObjectTarget
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

func (e *fileIngestEventEmitter) setTarget(target *commonv1.MediaObjectTarget) {
	if e == nil {
		return
	}
	e.target = target
}

func (e *fileIngestEventEmitter) setUploadIdentity(session model.UploadSession) {
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

func (e *fileIngestEventEmitter) progress(progress int32, bytesCompleted *int64) *managev1.FileIngestProgress {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	next := &managev1.FileIngestProgress{
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

func (e *fileIngestEventEmitter) nextEventFields() (string, *managev1.FileIngestIdentity, int64, int64, bool) {
	if e.nextSequence != nil && e.uploadID != "" {
		sequence, err := e.nextSequence(e.ctx, e.uploadID)
		if err != nil {
			slog.Warn("Failed to allocate file ingest event sequence", "error", err, "uploadId", e.uploadID, "fileId", e.fileID)
			return "", nil, 0, 0, false
		}
		e.sequence = sequence
	} else {
		e.sequence += 1
	}
	return e.correlationID,
		&managev1.FileIngestIdentity{
			EntityType:            e.entityType,
			EntityId:              e.entityID,
			FileId:                e.fileID,
			Target:                e.target,
			Source:                e.source,
			MediaKind:             e.mediaKind,
			UploadId:              optionalIngestString(e.uploadID),
			SlotId:                optionalIngestString(e.slotID),
			AttemptId:             optionalIngestString(e.attemptID),
			ExpectedCurrentFileId: e.expectedFileID,
		},
		e.sequence,
		time.Now().UnixMilli(),
		true
}

func publishFileIngestEvent(ctx context.Context, publisher AsyncPublisher, event proto.Message) error {
	if err := mq.ValidateFileIngestSignal(event); err != nil {
		return err
	}
	if attached, ok := event.(*managev1.FileIngestAttachedEvent); ok {
		messageID, err := mq.FileIngestAttachedMessageID(attached)
		if err != nil {
			return err
		}
		if err := publishDurableProto(
			ctx, publisher, eventpkg.QueueReleaseTrackOriginalAudioProjection, messageID, attached,
		); err != nil {
			return err
		}
	}
	return publishSignalProto(ctx, publisher, eventpkg.SignalFileIngest, event)
}

func (e *fileIngestEventEmitter) publishWithResult(
	event proto.Message,
	eventName string,
	progress int32,
) error {
	if e == nil {
		return nil
	}

	if err := publishFileIngestEvent(e.ctx, e.publisher, event); err != nil {
		slog.Warn("Failed to publish file ingest lifecycle event",
			"error", err,
			"event", eventName,
			"entityType", e.entityType.String(),
			"entityId", e.entityID,
			"fileId", e.fileID,
			"uploadId", e.uploadID,
			"slotId", e.slotID,
			"attemptId", e.attemptID,
			"correlationId", e.correlationID,
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

func (e *fileIngestEventEmitter) publish(event proto.Message, eventName string, progress int32) {
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
	event := &managev1.FileIngestUploadEvent{
		CorrelationId:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMs:    timestampMs,
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
	event := &managev1.FileIngestDownloadEvent{
		CorrelationId:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMs:    timestampMs,
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
	event := &managev1.FileIngestFinalizedEvent{
		CorrelationId:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMs:    timestampMs,
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
	event := &managev1.FileIngestAttachedEvent{
		CorrelationId:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMs:    timestampMs,
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
	event := &managev1.FileIngestFailedEvent{
		CorrelationId:  correlationID,
		Identity:       identity,
		SequenceNumber: sequence,
		TimestampMs:    timestampMs,
		Reason:         fileIngestFailureReason(errText),
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
