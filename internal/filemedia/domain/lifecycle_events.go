package domain

import "context"

type IngestEventKind string

const (
	IngestUploading   IngestEventKind = "uploading"
	IngestDownloading IngestEventKind = "downloading"
	IngestFinalized   IngestEventKind = "finalized"
	IngestAttached    IngestEventKind = "attached"
	IngestFailed      IngestEventKind = "failed"
)

type IngestIdentity struct {
	EntityType, Source, MediaKind                      int32
	EntityID, FileID                                   string
	Target                                             *ObjectTarget
	UploadID, SlotID, AttemptID, ExpectedCurrentFileID *string
}

type IngestProgress struct {
	Percentage                 int32
	BytesCompleted, BytesTotal *int64
}

type IngestEvent struct {
	Kind                        IngestEventKind
	CorrelationID               string
	Identity                    *IngestIdentity
	SequenceNumber, TimestampMS int64
	Progress                    *IngestProgress
	FileName, MimeType          string
	FileSize                    int64
	Reason                      int32
	Error                       string
}

type ProcessingOutputs struct {
	HLSGenerationID, ThumbnailAssetID, SpectrogramAssetID, WaveformAssetID *string
	DurationSeconds                                                        *int32
}

type ProcessingEvent struct {
	CorrelationID, EntityID, FileID       string
	EntityType, Status                    int32
	Outputs                               *ProcessingOutputs
	TimestampMS                           int64
	SlotID, AttemptID, TrackID, ReleaseID *string
}

type LifecyclePublisher interface {
	PublishIngest(context.Context, IngestEvent) error
	PublishProcessing(context.Context, ProcessingEvent) error
}
