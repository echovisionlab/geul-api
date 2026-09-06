//go:build integration

package filemedia

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type fileIngestFinalizerRecordingTranscoder struct {
	audio []*managev1.TranscodeAudioEvent
	video []*managev1.TranscodeVideoEvent
	err   error
}

func (p *fileIngestFinalizerRecordingTranscoder) PublishTranscodeAudio(
	_ context.Context,
	event *managev1.TranscodeAudioEvent,
) error {
	p.audio = append(p.audio, event)
	return p.err
}

func (p *fileIngestFinalizerRecordingTranscoder) PublishTranscodeVideo(
	_ context.Context,
	event *managev1.TranscodeVideoEvent,
) error {
	p.video = append(p.video, event)
	return p.err
}

func (*fileIngestFinalizerRecordingTranscoder) PublishWaveformCancel(
	context.Context,
	*managev1.WaveformCancelEvent,
) error {
	return nil
}

func (p *fileIngestFinalizerRecordingTranscoder) RegisterTranscodeAudio(
	_ context.Context,
	_ *gorm.DB,
	event *managev1.TranscodeAudioEvent,
) error {
	p.audio = append(p.audio, event)
	return p.err
}

func (p *fileIngestFinalizerRecordingTranscoder) RegisterTranscodeVideo(
	_ context.Context,
	_ *gorm.DB,
	event *managev1.TranscodeVideoEvent,
) error {
	p.video = append(p.video, event)
	return p.err
}

func TestAttachTrackOriginalAudioAppliesCASThenUsesStableTranscodeCommand(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	releaseID := testutil.CreateReleaseFixture(t, stack.DB)
	trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Projection finalizer track")

	oldFile := seedFileIngestFinalizerFile(t, stack, nil)
	attemptID := uuid.NewString()
	newFile := seedFileIngestFinalizerFile(t, stack, &attemptID)
	require.NoError(t, stack.DB.Model(&model.Track{}).
		Where("id = ?", trackID).
		Updates(structured.Fields{
			"audio_original_file_id": oldFile.ID,
			"processing_status":      managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_COMPLETED.String(),
		}).Error)
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
	require.NoError(t, stack.DB.Create(&model.FileIngestBinding{
		FileID: newFile.ID, UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
		EntityType: &entityType, EntityID: trackID,
	}).Error)

	asyncPublisher := &hardCutAsyncPublisher{}
	transcoder := &fileIngestFinalizerRecordingTranscoder{}
	files := NewFileService(
		stack.DB,
		runtimeS3Client(t, stack),
		asyncPublisher,
		stack.S3MediaBucket,
		stack.CDNURL,
		stack.MediaURL,
		stack.MediaSigningSecret,
		transcoder,
		stack.SpiceDBClient,
		directTrackAttachmentOption(stack.DB),
	)
	request := &intrav1.AttachTrackOriginalAudioRequest{
		TrackId:               trackID,
		VerifiedFileId:        newFile.ID,
		IngestAttemptId:       attemptID,
		ExpectedCurrentFileId: &oldFile.ID,
	}

	internalService := NewInternalFileIngestService(files)
	succeededResponse, err := internalService.AttachTrackOriginalAudio(t.Context(), connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_APPLIED, succeededResponse.Msg.GetResult())
	require.Equal(t, newFile.ID, succeededResponse.Msg.GetCurrentFileId())
	require.Equal(t, releaseID, succeededResponse.Msg.GetReleaseId())

	var track model.Track
	require.NoError(t, stack.DB.Where("id = ?", trackID).Take(&track).Error)
	require.NotNil(t, track.AudioOriginalFileID)
	require.Equal(t, newFile.ID, *track.AudioOriginalFileID)
	require.NotNil(t, track.ProcessingStatus)
	require.Equal(t, managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_PROCESSING.String(), *track.ProcessingStatus)

	var oldStored model.File
	require.NoError(t, stack.DB.Where("id = ?", oldFile.ID).Take(&oldStored).Error)
	require.Nil(t, oldStored.DeleteRequestedAt)

	queued := decodeHardCutRoutedMessages(
		t, asyncPublisher.messages, "", eventpkg.QueueTranscoderAudio,
		func() *managev1.TranscodeAudioEvent { return &managev1.TranscodeAudioEvent{} },
	)
	require.Len(t, queued, 1)
	require.NotEmpty(t, queued[0].GetEventId())
	require.NotEmpty(t, queued[0].GetHlsOutput().GetGenerationId())
	require.NotEmpty(t, queued[0].GetSpectrogramOutput().GetAssetId())

	duplicate, err := internalService.AttachTrackOriginalAudio(t.Context(), connect.NewRequest(request))
	require.NoError(t, err)
	require.Equal(t, intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_ALREADY_APPLIED, duplicate.Msg.GetResult())
	require.Equal(t, newFile.ID, duplicate.Msg.GetCurrentFileId())
	require.Equal(t, releaseID, duplicate.Msg.GetReleaseId())
	require.Len(t, asyncPublisher.messages, 1)
}

func TestAttachTrackOriginalAudioRejectsStaleCASWithoutSideEffects(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	releaseID := testutil.CreateReleaseFixture(t, stack.DB)
	trackID := testutil.CreateManagedReleaseTrack(t, stack.DB, releaseID, "Stale projection track")

	expected := seedFileIngestFinalizerFile(t, stack, nil)
	current := seedFileIngestFinalizerFile(t, stack, nil)
	attemptID := uuid.NewString()
	projected := seedFileIngestFinalizerFile(t, stack, &attemptID)
	require.NoError(t, stack.DB.Model(&model.Track{}).Where("id = ?", trackID).
		Update("audio_original_file_id", current.ID).Error)
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
	require.NoError(t, stack.DB.Create(&model.FileIngestBinding{
		FileID: projected.ID, UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
		EntityType: &entityType, EntityID: trackID,
	}).Error)
	transcoder := &fileIngestFinalizerRecordingTranscoder{}
	files := NewFileService(
		stack.DB, runtimeS3Client(t, stack), &hardCutAsyncPublisher{},
		stack.S3MediaBucket, stack.CDNURL, stack.MediaURL, stack.MediaSigningSecret,
		transcoder, stack.SpiceDBClient,
		directTrackAttachmentOption(stack.DB),
	)

	response, err := NewInternalFileIngestService(files).AttachTrackOriginalAudio(
		t.Context(),
		connect.NewRequest(&intrav1.AttachTrackOriginalAudioRequest{
			TrackId: trackID, VerifiedFileId: projected.ID, IngestAttemptId: attemptID,
			ExpectedCurrentFileId: &expected.ID,
		}),
	)
	require.Nil(t, response)
	require.ErrorContains(t, err, "Track original audio changed")
	require.Empty(t, transcoder.audio)

	var track model.Track
	require.NoError(t, stack.DB.Where("id = ?", trackID).Take(&track).Error)
	require.NotNil(t, track.AudioOriginalFileID)
	require.Equal(t, current.ID, *track.AudioOriginalFileID)
	var expectedStored model.File
	require.NoError(t, stack.DB.Where("id = ?", expected.ID).Take(&expectedStored).Error)
	require.Nil(t, expectedStored.DeleteRequestedAt)
}

func seedFileIngestFinalizerFile(
	t *testing.T,
	stack *testutil.RuntimeStack,
	attemptID *string,
) model.File {
	t.Helper()
	digest := make([]byte, 32)
	digest[0] = 1
	file := model.File{
		ID: uuid.NewString(), FileName: "field-recording", MimeType: "audio/wav",
		FileSize: 1024, Extension: "wav", SHA256: digest, IngestAttemptID: attemptID,
	}
	require.NoError(t, stack.DB.Create(&file).Error)
	return file
}
