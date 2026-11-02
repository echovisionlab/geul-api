package application_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	"github.com/echovisionlab/geul-api/internal/transcode/application/applicationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type publisher struct {
	lifecycleErr error
	lifecycle    []*managev1.MediaProcessingLifecycleEvent
	waveforms    []*managev1.WaveformGenerateEvent
}

func (p *publisher) PublishMediaProcessingLifecycle(_ context.Context, event *managev1.MediaProcessingLifecycleEvent) error {
	if p.lifecycleErr != nil {
		return p.lifecycleErr
	}
	p.lifecycle = append(p.lifecycle, event)
	return nil
}

func (p *publisher) PublishWaveformGenerate(_ context.Context, event *managev1.WaveformGenerateEvent) error {
	p.waveforms = append(p.waveforms, event)
	return nil
}

type tracker struct {
	completed []*managev1.TranscodeCompleteEvent
}

func (*tracker) HandleTranscodeProgress(context.Context, *managev1.TranscodeProgressEvent) error {
	return nil
}

func (t *tracker) HandleTranscodeComplete(_ context.Context, event *managev1.TranscodeCompleteEvent) error {
	t.completed = append(t.completed, event)
	return nil
}

func (*tracker) MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error {
	return nil
}

func seedFile(store *applicationtest.Store, extension, mimeType string) string {
	id := uuid.NewString()
	store.SeedFile(applicationtest.File{File: application.File{ID: id, MimeType: mimeType}, Extension: extension})
	return id
}

func source(t *testing.T, fileID, extension, mimeType string) *commonv1.MediaObjectTarget {
	t.Helper()
	key, err := mediaauth.MediaObjectKey(fileID, extension)
	require.NoError(t, err)
	return &commonv1.MediaObjectTarget{FileId: fileID, ObjectKey: key, Extension: extension, MimeType: mimeType}
}

func trackedJob(t *testing.T, store *applicationtest.Store, queueName, entityID, fileID, status string, event proto.Message, eventID string) {
	t.Helper()
	payload, err := proto.Marshal(event)
	require.NoError(t, err)
	store.SeedJob(transcode.Job{
		EventID: eventID, QueueName: queueName, EntityType: "TRANSCODE_ENTITY_TYPE_POST", EntityID: entityID,
		FileID: fileID, Payload: payload, Status: status,
	})
}

func marshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(message)
	require.NoError(t, err)
	return body
}

func TestTranscodeCompletionOfAFilePendingDeletionOnlyRetiresItsJob(t *testing.T) {
	store, units := applicationtest.New()
	recorder := &publisher{}
	handlers := application.New(units, recorder, &tracker{})
	fileID, postID := seedFile(store, "mp3", "audio/mpeg"), uuid.NewString()
	hls := store.SeedAllocatedGeneration(fileID)
	spectrogram := store.SeedAllocatedAsset("spectrogram", fileID, "png", "image/png")
	jobID := uuid.NewString()
	trackedJob(t, store, eventpkg.QueueTranscoderAudio, postID, fileID, transcode.StatusQueued, &managev1.TranscodeAudioEvent{
		EventId: jobID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID,
		Source: source(t, fileID, "mp3", "audio/mpeg"), HlsOutput: hls, SpectrogramOutput: spectrogram,
	}, jobID)
	store.SeedWaveformJob(transcode.WaveformJob{
		EventID: uuid.NewString(), EntityType: "TRANSCODE_ENTITY_TYPE_POST", EntityID: postID, FileID: fileID,
		Status: transcode.WaveformJobStatusQueued,
	})
	store.RequestFileDeletion(fileID)
	duration := int32(157)

	require.NoError(t, handlers.HandleTranscodeComplete(t.Context(), marshal(t, &managev1.TranscodeCompleteEvent{
		EventId: jobID, FileId: fileID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, Success: true, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		Outputs: &managev1.TranscodeOutputs{
			Hls:             &commonv1.MediaGenerationWriteResult{GenerationId: hls.GetGenerationId()},
			Spectrogram:     &commonv1.AssetWriteResult{AssetId: spectrogram.GetAssetId()},
			DurationSeconds: &duration,
		},
	})))
	_, found := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_HLS")
	require.False(t, found)
	_, found = store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_SPECTROGRAM")
	require.False(t, found)
	_, found = store.StoredJob(jobID)
	require.False(t, found)
	require.Len(t, store.WaveformJobsOf(fileID), 1, "one terminal result must not erase another active writer authority")
	require.Empty(t, recorder.lifecycle)
	require.Empty(t, recorder.waveforms)
}

func TestTranscodeCompletionRetriesItsOutputsBeforeFinishingTheTracker(t *testing.T) {
	store, units := applicationtest.New()
	recorder := &publisher{lifecycleErr: errors.New("lifecycle unavailable")}
	jobs := &tracker{}
	handlers := application.New(units, recorder, jobs)
	fileID, postID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	hls := store.SeedAllocatedGeneration(fileID)
	spectrogram := store.SeedAllocatedAsset("spectrogram", fileID, "png", "image/png")
	jobID := uuid.NewString()
	trackedJob(t, store, eventpkg.QueueTranscoderAudio, postID, fileID, transcode.StatusQueued, &managev1.TranscodeAudioEvent{
		EventId: jobID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID,
		Source: source(t, fileID, "ogg", "audio/ogg"), HlsOutput: hls, SpectrogramOutput: spectrogram,
	}, jobID)
	duration := int32(42)
	body := marshal(t, &managev1.TranscodeCompleteEvent{
		EventId: jobID, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls:             applicationtest.WrittenGeneration(hls, 0x11),
			Spectrogram:     applicationtest.Written(spectrogram, 0x22),
			DurationSeconds: &duration,
		},
	})

	require.ErrorContains(t, handlers.HandleTranscodeComplete(t.Context(), body), "lifecycle unavailable")
	require.Empty(t, jobs.completed)
	require.Len(t, recorder.waveforms, 1)
	hlsDerivative, _ := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_HLS")
	require.Equal(t, hls.GetGenerationId(), *hlsDerivative.MediaGenerationID)
	spectrogramDerivative, _ := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_SPECTROGRAM")
	require.Equal(t, spectrogram.GetAssetId(), *spectrogramDerivative.AssetID)
	require.Equal(t, 42, *store.StoredFile(fileID).DurationSeconds)

	recorder.lifecycleErr = nil
	require.NoError(t, handlers.HandleTranscodeComplete(t.Context(), body))
	require.Len(t, jobs.completed, 1)
	require.Len(t, recorder.waveforms, 1, "a retry does not enqueue a second waveform")
	require.Len(t, recorder.lifecycle, 1)
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING, recorder.lifecycle[0].GetStatus())
	require.Equal(t, application.GenerationReady, store.StoredGeneration(hls.GetGenerationId()).Status)
	waveform := recorder.waveforms[0]
	require.Equal(t, fileID, waveform.GetSource().GetFileId())
	require.NoError(t, transcode.ValidateWaveformGenerateEvent(waveform))
}

func TestASupersededTranscodeCompletionRetiresItsOutputsWithoutSwitchingDerivatives(t *testing.T) {
	store, units := applicationtest.New()
	recorder := &publisher{}
	jobs := &tracker{}
	handlers := application.New(units, recorder, jobs)
	fileID, postID := seedFile(store, "mp4", "video/mp4"), uuid.NewString()
	hls := store.SeedAllocatedGeneration(fileID)
	thumbnail := store.SeedAllocatedAsset("thumbnail", fileID, "webp", "image/webp")
	jobID := uuid.NewString()
	trackedJob(t, store, eventpkg.QueueTranscoderVideo, postID, fileID, transcode.StatusCancelled, &managev1.TranscodeVideoEvent{
		EventId: jobID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID,
		Source: source(t, fileID, "mp4", "video/mp4"), HlsOutput: hls, ThumbnailOutput: thumbnail,
	}, jobID)

	require.NoError(t, handlers.HandleTranscodeComplete(t.Context(), marshal(t, &managev1.TranscodeCompleteEvent{
		EventId: jobID, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls:       applicationtest.WrittenGeneration(hls, 0x31),
			Thumbnail: applicationtest.Written(thumbnail, 0x32),
		},
	})))
	require.Len(t, jobs.completed, 1)
	retired := store.StoredGeneration(hls.GetGenerationId())
	require.Equal(t, application.GenerationRetired, retired.Status)
	require.NotNil(t, retired.DeleteAfter)
	require.Equal(t, bytes.Repeat([]byte{0x31}, 32), retired.ManifestSHA256)
	asset, _ := store.StoredAsset(thumbnail.GetAssetId())
	require.Equal(t, application.AssetDeletePending, asset.Status)
	_, found := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_HLS")
	require.False(t, found)
	_, found = store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_THUMBNAIL")
	require.False(t, found)
	require.Empty(t, recorder.lifecycle)
}

func waveformJob(t *testing.T, store *applicationtest.Store, units mutation.UnitOfWork[application.Store], fileID, postID string) *managev1.WaveformGenerateEvent {
	t.Helper()
	output := store.SeedAllocatedAsset("waveform", fileID, "json", "application/json")
	job := &managev1.WaveformGenerateEvent{
		EventId: output.GetAssetId(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: postID, FileId: fileID, Source: source(t, fileID, "ogg", "audio/ogg"), Output: output,
	}
	store.SeedWaveformJob(transcode.WaveformJob{
		EventID: job.EventId, EntityType: job.EntityType.String(), EntityID: postID, FileID: fileID,
		Status: transcode.WaveformJobStatusQueued,
	})
	return job
}

func TestADuplicateWaveformCompletionKeepsTheReferencedAssetReady(t *testing.T) {
	store, units := applicationtest.New()
	recorder := &publisher{}
	handlers := application.New(units, recorder, &tracker{})
	fileID, postID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	job := waveformJob(t, store, units, fileID, postID)
	body := marshal(t, &managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: applicationtest.Written(job.Output, 0x33),
	})

	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), body))
	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), body))
	derivative, _ := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_WAVEFORM")
	require.Equal(t, job.Output.GetAssetId(), *derivative.AssetID)
	asset, _ := store.StoredAsset(job.Output.GetAssetId())
	require.Equal(t, application.AssetReady, asset.Status)
	require.Len(t, recorder.lifecycle, 1)
	require.Empty(t, store.WaveformJobsOf(fileID))
}

func TestAWaveformCompletionForAFilePendingDeletionRetiresItsOutput(t *testing.T) {
	store, units := applicationtest.New()
	handlers := application.New(units, &publisher{}, &tracker{})
	fileID, postID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	job := waveformJob(t, store, units, fileID, postID)
	store.RequestFileDeletion(fileID)

	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), marshal(t, &managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: applicationtest.Written(job.Output, 0x44),
	})))
	_, found := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_WAVEFORM")
	require.False(t, found)
	asset, _ := store.StoredAsset(job.Output.GetAssetId())
	require.Equal(t, application.AssetDeletePending, asset.Status)
	require.Empty(t, store.WaveformJobsOf(fileID))
}

func TestAWaveformCompletionRetiresTheUnboundAssetItReplaces(t *testing.T) {
	store, units := applicationtest.New()
	handlers := application.New(units, &publisher{}, &tracker{})
	fileID, postID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	previous := store.SeedAllocatedAsset("waveform", fileID, "json", "application/json")
	require.NoError(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[application.Store]) error {
		if _, err := tx.Store().CompleteAsset(t.Context(), applicationtest.Written(previous, 0x66)); err != nil {
			return err
		}
		return tx.Store().UpsertDerivative(t.Context(), application.Derivative{
			ID: uuid.NewString(), FileID: fileID, Type: "FILE_DERIVATIVE_TYPE_WAVEFORM", AssetID: &previous.AssetId,
		})
	}))
	job := waveformJob(t, store, units, fileID, postID)

	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), marshal(t, &managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: applicationtest.Written(job.Output, 0x77),
	})))
	derivative, _ := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_WAVEFORM")
	require.Equal(t, job.Output.GetAssetId(), *derivative.AssetID)
	replaced, _ := store.StoredAsset(previous.GetAssetId())
	require.Equal(t, application.AssetDeletePending, replaced.Status)
}

func TestAWaveformCompletionKeepsAReplacedAssetSomethingStillBinds(t *testing.T) {
	store, units := applicationtest.New()
	handlers := application.New(units, &publisher{}, &tracker{})
	fileID, postID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	previous := store.SeedAllocatedAsset("waveform", fileID, "json", "application/json")
	require.NoError(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[application.Store]) error {
		if _, err := tx.Store().CompleteAsset(t.Context(), applicationtest.Written(previous, 0x66)); err != nil {
			return err
		}
		return tx.Store().UpsertDerivative(t.Context(), application.Derivative{
			ID: uuid.NewString(), FileID: fileID, Type: "FILE_DERIVATIVE_TYPE_WAVEFORM", AssetID: &previous.AssetId,
		})
	}))
	store.SeedBinding(previous.GetAssetId())
	job := waveformJob(t, store, units, fileID, postID)

	require.NoError(t, handlers.HandleWaveformComplete(t.Context(), marshal(t, &managev1.WaveformCompleteEvent{
		EventId: job.EventId, EntityType: job.EntityType, EntityId: postID, FileId: fileID,
		Output: applicationtest.Written(job.Output, 0x77),
	})))
	kept, _ := store.StoredAsset(previous.GetAssetId())
	require.Equal(t, application.AssetReady, kept.Status)
}

func TestATrackTranscodeFailureSettlesTheTrackAndPublishesTheFailure(t *testing.T) {
	store, units := applicationtest.New()
	recorder := &publisher{}
	handlers := application.New(units, recorder, &tracker{})
	fileID, trackID := seedFile(store, "ogg", "audio/ogg"), uuid.NewString()
	store.SeedTrack(trackID, applicationtest.Track{ReleaseID: "release", SourceFileID: fileID, Status: application.TrackProcessing})
	jobID := uuid.NewString()
	payload, err := proto.Marshal(&managev1.TranscodeAudioEvent{EventId: jobID})
	require.NoError(t, err)
	store.SeedJob(transcode.Job{
		EventID: jobID, QueueName: eventpkg.QueueTranscoderAudio, EntityType: "TRANSCODE_ENTITY_TYPE_TRACK",
		EntityID: trackID, FileID: fileID, Payload: payload, Status: transcode.StatusProcessing,
	})
	failure := " decoder crashed "

	require.NoError(t, handlers.HandleTranscodeComplete(t.Context(), marshal(t, &managev1.TranscodeCompleteEvent{
		EventId: jobID, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, EntityId: trackID, FileId: fileID, Error: &failure,
	})))
	job, _ := store.StoredJob(jobID)
	require.Equal(t, transcode.StatusFailedTerminal, job.Status)
	require.Equal(t, "decoder crashed", *job.LastError)
	require.Equal(t, application.TrackFailed, store.StoredTrack(trackID).Status)
	require.Len(t, recorder.lifecycle, 1)
	event := recorder.lifecycle[0]
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED, event.GetStatus())
	require.Equal(t, trackID, event.GetTrackId())
	require.Equal(t, "release", event.GetReleaseId())
}

func TestATranscodeCompletionNamingAnotherFilesGenerationChangesNothing(t *testing.T) {
	store, units := applicationtest.New()
	jobs := &tracker{}
	handlers := application.New(units, &publisher{}, jobs)
	fileID, otherID, postID := seedFile(store, "mp4", "video/mp4"), seedFile(store, "mp4", "video/mp4"), uuid.NewString()
	hls := store.SeedAllocatedGeneration(fileID)
	store.ReassignGeneration(hls.GetGenerationId(), otherID)
	thumbnail := store.SeedAllocatedAsset("thumbnail", fileID, "webp", "image/webp")
	jobID := uuid.NewString()
	trackedJob(t, store, eventpkg.QueueTranscoderVideo, postID, fileID, transcode.StatusQueued, &managev1.TranscodeVideoEvent{
		EventId: jobID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID,
		Source: source(t, fileID, "mp4", "video/mp4"), HlsOutput: hls, ThumbnailOutput: thumbnail,
	}, jobID)

	err := handlers.HandleTranscodeComplete(t.Context(), marshal(t, &managev1.TranscodeCompleteEvent{
		EventId: jobID, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, EntityId: postID, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls:       applicationtest.WrittenGeneration(hls, 0x41),
			Thumbnail: applicationtest.Written(thumbnail, 0x42),
		},
	}))
	require.ErrorContains(t, err, "is not allocated for source file")
	require.Equal(t, application.GenerationAllocated, store.StoredGeneration(hls.GetGenerationId()).Status)
	asset, _ := store.StoredAsset(thumbnail.GetAssetId())
	require.Equal(t, application.AssetAllocated, asset.Status, "the thumbnail completion rolls back with the generation")
	_, found := store.StoredDerivative(fileID, "FILE_DERIVATIVE_TYPE_THUMBNAIL")
	require.False(t, found)
	require.Empty(t, jobs.completed)
}
