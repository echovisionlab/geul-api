package domain

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

type TrackOriginalAudioInput struct {
	ClientMediaReady        bool
	DurationSeconds         *int
	TrackID, VerifiedFileID string
	ExpectedCurrentFileID   *string
}

type TrackOriginalAudioAttachment struct {
	AlreadyApplied           bool
	CurrentFileID, ReleaseID string
}

type IngestTranscodeKind uint8

const (
	IngestTranscodeAudio IngestTranscodeKind = iota + 1
	IngestTranscodeVideo
)

type IngestTranscodeCommand struct {
	Kind                      IngestTranscodeKind
	EventID, FileID, EntityID string
	EntityType                int32
	Source                    *ObjectTarget
	HLSOutput                 *GenerationTarget
	ArtifactOutput            *mediaasset.WriteTarget
}

type IngestDerivative struct {
	ID, FileID, Type      string
	AssetID, GenerationID *string
	CreatedAt             time.Time
}

type IngestMediaStore interface {
	LockIngestFile(context.Context, string) error
	EnsureIngestGeneration(context.Context, mediaasset.Generation) (mediaasset.Generation, error)
	EnsureIngestAsset(context.Context, mediaasset.Asset) (mediaasset.Asset, error)
	CompleteIngestGeneration(context.Context, string, mediaasset.WrittenGeneration) error
	CompleteIngestAsset(context.Context, string, mediaasset.WrittenAsset) error
	UpsertIngestDerivative(context.Context, IngestDerivative) error
	SetClientMediaBundle(context.Context, string, string, int) error
	ReadyIngestDerivativeCount(context.Context, string, []string) (int64, error)
	AttachIngestTrackAudio(context.Context, TrackOriginalAudioInput) (TrackOriginalAudioAttachment, error)
	AttachedIngestTrackRelease(context.Context, string, string) (string, error)
	RequireIngestTranscodePublisher(context.Context) error
	EnqueueIngestTranscode(context.Context, IngestTranscodeCommand) error
}
