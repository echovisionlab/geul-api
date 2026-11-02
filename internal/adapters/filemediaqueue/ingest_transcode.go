package filemediaqueue

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type ingestRegistrar interface {
	RegisterTranscodeAudio(context.Context, *gorm.DB, *managev1.TranscodeAudioEvent) error
	RegisterTranscodeVideo(context.Context, *gorm.DB, *managev1.TranscodeVideoEvent) error
}

type IngestTranscodes struct{ publisher, async any }

func Transcodes(publisher, async any) IngestTranscodes { return IngestTranscodes{publisher, async} }

func (p IngestTranscodes) RequirePublisher() error {
	if _, ok := p.publisher.(ingestRegistrar); !ok {
		return fmt.Errorf("transcode publisher does not register File ingest jobs")
	}
	return nil
}

func (p IngestTranscodes) Enqueue(ctx context.Context, tx *gorm.DB, input domain.IngestTranscodeCommand) error {
	if err := p.RequirePublisher(); err != nil {
		return err
	}
	registrar := p.publisher.(ingestRegistrar)
	var message proto.Message
	var queue string
	switch input.Kind {
	case domain.IngestTranscodeAudio:
		event := AudioIngestEvent(input)
		if err := registrar.RegisterTranscodeAudio(ctx, tx, event); err != nil {
			return err
		}
		message, queue = event, eventpkg.QueueTranscoderAudio
	case domain.IngestTranscodeVideo:
		event := VideoIngestEvent(input)
		if err := registrar.RegisterTranscodeVideo(ctx, tx, event); err != nil {
			return err
		}
		message, queue = event, eventpkg.QueueTranscoderVideo
	default:
		return fmt.Errorf("unsupported File ingest transcode kind %d", input.Kind)
	}
	executor, err := contentsignal.TransactionExecutor(tx)
	if err != nil {
		return err
	}
	async, ok := p.async.(interface {
		EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
	})
	if !ok {
		return fmt.Errorf("async publisher does not support transactional PGMQ enqueue")
	}
	return async.EnqueueProtobufWithExecutor(ctx, executor, queue, input.EventID, message)
}

func AudioIngestEvent(input domain.IngestTranscodeCommand) *managev1.TranscodeAudioEvent {
	source, hls, asset := ingestTargets(input)
	return &managev1.TranscodeAudioEvent{EventId: input.EventID, EntityType: managev1.TranscodeEntityType(input.EntityType), EntityId: input.EntityID, FileId: input.FileID, Source: source, HlsOutput: hls, SpectrogramOutput: asset}
}

func VideoIngestEvent(input domain.IngestTranscodeCommand) *managev1.TranscodeVideoEvent {
	source, hls, asset := ingestTargets(input)
	return &managev1.TranscodeVideoEvent{EventId: input.EventID, EntityType: managev1.TranscodeEntityType(input.EntityType), EntityId: input.EntityID, FileId: input.FileID, Source: source, HlsOutput: hls, ThumbnailOutput: asset}
}

func ingestTargets(input domain.IngestTranscodeCommand) (*commonv1.MediaObjectTarget, *commonv1.MediaGenerationWriteTarget, *commonv1.AssetWriteTarget) {
	var source *commonv1.MediaObjectTarget
	if v := input.Source; v != nil {
		source = &commonv1.MediaObjectTarget{FileId: v.FileID, ObjectKey: v.ObjectKey, Extension: v.Extension, MimeType: v.MimeType}
	}
	var hls *commonv1.MediaGenerationWriteTarget
	if v := input.HLSOutput; v != nil {
		hls = &commonv1.MediaGenerationWriteTarget{GenerationId: v.GenerationID, FileId: v.FileID, ObjectPrefix: v.ObjectPrefix}
	}
	var asset *commonv1.AssetWriteTarget
	if v := input.ArtifactOutput; v != nil {
		asset = &commonv1.AssetWriteTarget{AssetId: v.AssetID, ObjectKey: v.ObjectKey, Extension: v.Extension, MimeType: v.MimeType, Disposition: commonv1.AssetDisposition(v.Disposition), DownloadFilename: v.DownloadFilename}
	}
	return source, hls, asset
}
