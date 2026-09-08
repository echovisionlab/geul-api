package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/transcoding/config"
	"github.com/echovisionlab/geul-api/internal/transcoding/ffmpeg"
	"github.com/echovisionlab/geul-api/internal/transcoding/hls"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/mq"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type fakeHandlerFFmpeg struct {
	workDir          string
	capturedAudioHLS ffmpeg.AudioHLSOptions
	capturedVideoHLS ffmpeg.HLSOptions
	createErr        error
	probeErr         error
	validateAudioErr error
	validateVideoErr error
	audioHLSErr      error
	videoHLSErr      error
	invalidAudioHLS  bool
	invalidVideoHLS  bool
	spectrogramErr   error
	thumbnailErrs    []error
	cleanupErr       error
}

func (f *fakeHandlerFFmpeg) CreateJobWorkDir(jobID string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	dir := filepath.Join(f.workDir, jobID)
	return dir, os.MkdirAll(dir, 0o755)
}
func (f *fakeHandlerFFmpeg) CleanupJobWorkDir(string) error { return f.cleanupErr }
func (f *fakeHandlerFFmpeg) Probe(context.Context, string) (*ffmpeg.ProbeResult, error) {
	return &ffmpeg.ProbeResult{DurationSeconds: 42}, f.probeErr
}
func (f *fakeHandlerFFmpeg) ValidateAudioFile(context.Context, string) error {
	return f.validateAudioErr
}
func (f *fakeHandlerFFmpeg) ValidateVideoFile(context.Context, string) error {
	return f.validateVideoErr
}
func (f *fakeHandlerFFmpeg) GenerateAudioHLSWithProgress(
	_ context.Context,
	_ string,
	outputDir string,
	opts ffmpeg.AudioHLSOptions,
	_ float64,
	onProgress ffmpeg.ProgressCallback,
) (*ffmpeg.HLSResult, error) {
	f.capturedAudioHLS = opts
	return fakeHLSResult(outputDir, onProgress, 50, f.audioHLSErr, f.invalidAudioHLS, writeAudioHLSPackage)
}
func (f *fakeHandlerFFmpeg) GenerateAudioSpectrogramWithProgress(
	_ context.Context,
	_ string,
	outputPath string,
	_ ffmpeg.SpectrogramOptions,
	_ float64,
	onProgress ffmpeg.ProgressCallback,
) error {
	if f.spectrogramErr != nil {
		return f.spectrogramErr
	}
	if onProgress != nil {
		onProgress(100)
	}
	return os.WriteFile(outputPath, []byte("spectrogram"), 0o644)
}
func (f *fakeHandlerFFmpeg) GenerateVideoThumbnail(_ context.Context, _, outputPath string, _ ffmpeg.VideoThumbnailOptions) error {
	if len(f.thumbnailErrs) > 0 {
		err := f.thumbnailErrs[0]
		f.thumbnailErrs = f.thumbnailErrs[1:]
		if err != nil {
			return err
		}
	}
	return os.WriteFile(outputPath, []byte("thumbnail"), 0o644)
}
func (f *fakeHandlerFFmpeg) GenerateHLSWithProgress(
	_ context.Context,
	_ string,
	outputDir string,
	opts ffmpeg.HLSOptions,
	_ float64,
	onProgress ffmpeg.ProgressCallback,
) (*ffmpeg.HLSResult, error) {
	f.capturedVideoHLS = opts
	return fakeHLSResult(outputDir, onProgress, 100, f.videoHLSErr, f.invalidVideoHLS, writeVideoHLSPackage)
}

func fakeHLSResult(outputDir string, onProgress ffmpeg.ProgressCallback, progress int, generationErr error, invalid bool, writePackage func(string) error) (*ffmpeg.HLSResult, error) {
	if generationErr != nil {
		return nil, generationErr
	}
	if onProgress != nil {
		onProgress(progress)
	}
	if err := writePackage(outputDir); err != nil {
		return nil, err
	}
	if invalid {
		if err := os.WriteFile(filepath.Join(outputDir, "master.m3u8"), []byte("invalid"), 0o644); err != nil {
			return nil, err
		}
	}
	return &ffmpeg.HLSResult{SegmentDir: outputDir}, nil
}

func writeAudioHLSPackage(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "segment_000.ts"), []byte("segment"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte("#EXTM3U\n#EXTINF:6,\nsegment_000.ts\n#EXT-X-ENDLIST\n"), 0o644)
}

func writeVideoHLSPackage(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stream_360p_000.ts"), []byte("segment"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stream_360p_001.ts"), []byte("segment"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "stream_360p.m3u8"), []byte("#EXTM3U\n#EXTINF:6,\nstream_360p_000.ts\n#EXTINF:6,\nstream_360p_001.ts\n#EXT-X-ENDLIST\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "master.m3u8"), []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nstream_360p.m3u8\n"), 0o644)
}

type fakeHandlerStorage struct {
	downloadErr    error
	uploadErr      error
	uploadErrMatch string
	removeOnUpload bool
}

func (s fakeHandlerStorage) Download(_ context.Context, _ string, localPath string) error {
	if s.downloadErr != nil {
		return s.downloadErr
	}
	return os.WriteFile(localPath, []byte("audio"), 0o644)
}
func (s fakeHandlerStorage) Upload(_ context.Context, key, localPath string, _ string) error {
	if s.uploadErr != nil && (s.uploadErrMatch == "" || strings.Contains(key, s.uploadErrMatch)) {
		return s.uploadErr
	}
	if s.removeOnUpload {
		return os.Remove(localPath)
	}
	return nil
}
func (s fakeHandlerStorage) UploadCompleted(ctx context.Context, key, localPath, contentType string, _ []byte) error {
	return s.Upload(ctx, key, localPath, contentType)
}
func (s fakeHandlerStorage) Completion(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

type recordingCleanupStorage struct {
	uploads       []string
	completed     map[string][]byte
	completionErr error
}

func (recordingCleanupStorage) Download(_ context.Context, _ string, localPath string) error {
	return os.WriteFile(localPath, []byte("media"), 0o644)
}

func (s *recordingCleanupStorage) Upload(_ context.Context, key, _, _ string) error {
	s.uploads = append(s.uploads, key)
	return nil
}
func (s *recordingCleanupStorage) UploadCompleted(ctx context.Context, key, localPath, contentType string, completion []byte) error {
	if err := s.Upload(ctx, key, localPath, contentType); err != nil {
		return err
	}
	if s.completed == nil {
		s.completed = make(map[string][]byte)
	}
	s.completed[key] = append([]byte(nil), completion...)
	return nil
}
func (s *recordingCleanupStorage) Completion(_ context.Context, key string) ([]byte, bool, error) {
	if s.completionErr != nil {
		return nil, false, s.completionErr
	}
	payload, found := s.completed[key]
	return append([]byte(nil), payload...), found, nil
}

type recordingHandlerPublisher struct {
	complete    *apiv1.TranscodeCompleteEvent
	progress    []*apiv1.TranscodeProgressEvent
	completeErr error
	progressErr error
}

func (p *recordingHandlerPublisher) PublishComplete(_ context.Context, event *apiv1.TranscodeCompleteEvent) error {
	p.complete = event
	return p.completeErr
}

func (p *recordingHandlerPublisher) PublishProgress(_ context.Context, event *apiv1.TranscodeProgressEvent) error {
	p.progress = append(p.progress, event)
	return p.progressErr
}

func audioJob(eventID, entityID, fileID string) *apiv1.TranscodeAudioEvent {
	eventID, entityID, fileID, generationID := jobIDs(eventID, entityID, fileID)
	assetID := testUUID("spectrogram-" + fileID)
	return &apiv1.TranscodeAudioEvent{
		EventId:    eventID,
		EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   entityID,
		FileId:     fileID,
		Source:     mediaTarget(fileID, "mp3", "audio/mpeg"),
		HlsOutput:  generationTarget(fileID, generationID),
		SpectrogramOutput: &commonv1.AssetWriteTarget{
			AssetId:     assetID,
			ObjectKey:   "asset/" + assetID + ".png",
			Extension:   "png",
			MimeType:    "image/png",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

func videoJob(eventID, entityID, fileID string) *apiv1.TranscodeVideoEvent {
	eventID, entityID, fileID, generationID := jobIDs(eventID, entityID, fileID)
	assetID := testUUID("thumbnail-" + fileID)
	return &apiv1.TranscodeVideoEvent{
		EventId:    eventID,
		EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   entityID,
		FileId:     fileID,
		Source:     mediaTarget(fileID, "mp4", "video/mp4"),
		HlsOutput:  generationTarget(fileID, generationID),
		ThumbnailOutput: &commonv1.AssetWriteTarget{
			AssetId:     assetID,
			ObjectKey:   "asset/" + assetID + ".webp",
			Extension:   "webp",
			MimeType:    "image/webp",
			Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

func jobIDs(eventID, entityID, fileID string) (string, string, string, string) {
	eventID = testUUID(eventID)
	entityID = testUUID(entityID)
	fileID = testUUID(fileID)
	return eventID, entityID, fileID, testUUID("generation-" + fileID)
}

func mediaTarget(fileID, extension, mimeType string) *commonv1.MediaObjectTarget {
	return &commonv1.MediaObjectTarget{
		FileId: fileID, ObjectKey: "media/" + fileID + "." + extension, Extension: extension, MimeType: mimeType,
	}
}

func generationTarget(fileID, generationID string) *commonv1.MediaGenerationWriteTarget {
	return &commonv1.MediaGenerationWriteTarget{GenerationId: generationID, FileId: fileID, ObjectPrefix: "media/" + fileID + "/hls/" + generationID}
}

func testUUID(value string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value)).String()
}

func TestProcessAudioJobAlwaysUsesDefaultPipelineWithConfiguredBitrate(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{workDir: workDir}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t,
		&config.Config{
			AudioHLSBitrate: "96k",
			JobTimeoutMins:  5,
		},
		ff,
		fakeHandlerStorage{},
		pub,
	)

	job := audioJob("job-1", "post-1", "file-1")

	err := h.HandleAudioJob(context.Background(), job)
	require.NoError(t, err)
	require.NotNil(t, pub.complete)
	expectedOptions := ffmpeg.DefaultAudioHLSOptions()
	expectedOptions.Bitrate = "96k"
	assert.Equal(t, expectedOptions, ff.capturedAudioHLS)
	assert.Equal(t, int32(42), pub.complete.GetOutputs().GetDurationSeconds())
	assert.NotNil(t, pub.complete.GetOutputs().GetHls())
	assert.NotNil(t, pub.complete.GetOutputs().GetSpectrogram())
	assert.True(t, pub.complete.GetSuccess())
	assert.Equal(t, job.GetEventId(), pub.complete.GetEventId())
	assert.Equal(t, job.GetEntityType(), pub.complete.GetEntityType())
	assert.Equal(t, job.GetEntityId(), pub.complete.GetEntityId())
	assert.Equal(t, job.GetFileId(), pub.complete.GetFileId())

	progressStages := make(map[apiv1.TranscodeStage]bool)
	var previousSequence int64
	for _, progress := range pub.progress {
		require.Greater(t, progress.GetSequenceNumber(), previousSequence)
		previousSequence = progress.GetSequenceNumber()
		require.Equal(t, job.GetEventId(), progress.GetEventId())
		require.Equal(t, job.GetEntityType(), progress.GetEntityType())
		require.Equal(t, job.GetEntityId(), progress.GetEntityId())
		require.Equal(t, job.GetFileId(), progress.GetFileId())
		require.NotNil(t, progress.Stage)
		progressStages[progress.GetStage()] = true
	}
	for _, stage := range []apiv1.TranscodeStage{
		apiv1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING,
		apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING,
		apiv1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_UPLOADING,
		apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING,
		apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING,
	} {
		assert.Truef(t, progressStages[stage], "expected progress stage %s", stage)
	}
}

func TestProcessAudioJobPublishesAllocatedOutputs(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{workDir: workDir}
	storageClient := &recordingCleanupStorage{}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t,
		&config.Config{
			AudioHLSBitrate: "128k",
			JobTimeoutMins:  5,
		},
		ff,
		storageClient,
		pub,
	)

	job := audioJob("job-clean-audio", "post-1", "file-clean")

	err := h.HandleAudioJob(context.Background(), job)
	require.NoError(t, err)
	require.Contains(t, storageClient.uploads, job.GetSpectrogramOutput().GetObjectKey())
	require.Contains(t, storageClient.uploads, job.GetHlsOutput().GetObjectPrefix()+"/master.m3u8")
	require.Equal(t, job.GetEventId(), pub.complete.GetEventId())
	require.Equal(t, job.GetSpectrogramOutput().GetAssetId(), pub.complete.GetOutputs().GetSpectrogram().GetAssetId())
	require.Equal(t, job.GetHlsOutput().GetGenerationId(), pub.complete.GetOutputs().GetHls().GetGenerationId())
}

func TestProcessAudioJobFailsWhenSpectrogramGenerationFails(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{
		workDir:        workDir,
		spectrogramErr: errors.New("boom"),
	}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t,
		&config.Config{
			AudioHLSBitrate: "128k",
			JobTimeoutMins:  5,
		},
		ff,
		fakeHandlerStorage{},
		pub,
	)

	job := audioJob("job-2", "post-1", "file-2")

	err := h.HandleAudioJob(context.Background(), job)
	require.Error(t, err)
	require.Contains(t, err.Error(), "audio spectrogram failed")
	require.NotNil(t, pub.complete)
	require.False(t, pub.complete.Success)
	require.NotNil(t, pub.complete.Error)
	require.Contains(t, *pub.complete.Error, "audio spectrogram failed")
	require.Equal(t, job.GetEventId(), pub.complete.GetEventId())
}

func TestProcessAudioJobPublishesHLSUploadingProgress(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{workDir: workDir}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t,
		&config.Config{
			AudioHLSBitrate: "128k",
			JobTimeoutMins:  5,
		},
		ff,
		fakeHandlerStorage{},
		pub,
	)

	job := audioJob("job-3", "post-1", "file-3")

	err := h.HandleAudioJob(context.Background(), job)
	require.NoError(t, err)
	require.NotNil(t, pub.complete)

	var sawUploadStage bool
	for _, event := range pub.progress {
		stage := event.GetStage()
		if stage == apiv1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING {
			sawUploadStage = true
			break
		}
	}

	require.True(t, sawUploadStage)
}

func TestProcessVideoJobAppliesRequestedResolutionsAndPublishesComplete(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{workDir: workDir}
	storageClient := &recordingCleanupStorage{}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t,
		&config.Config{JobTimeoutMins: 5},
		ff,
		storageClient,
		pub,
	)

	job := videoJob("video-job-1", "post-1", "video-file-1")
	job.Options = &apiv1.VideoTranscodeOptions{
		GenerateThumbnail: true,
		Resolutions: []apiv1.VideoResolution{
			apiv1.VideoResolution_VIDEO_RESOLUTION_360P,
			apiv1.VideoResolution_VIDEO_RESOLUTION_720P,
		},
	}

	err := h.HandleVideoJob(context.Background(), job)
	require.NoError(t, err)
	require.NotNil(t, pub.complete)
	require.True(t, pub.complete.Success)
	require.NotNil(t, pub.complete.Outputs)
	require.Equal(t, job.GetThumbnailOutput().GetAssetId(), pub.complete.Outputs.GetThumbnail().GetAssetId())
	require.Equal(t, job.GetHlsOutput().GetGenerationId(), pub.complete.Outputs.GetHls().GetGenerationId())
	require.Len(t, ff.capturedVideoHLS.Resolutions, 2)
	require.Equal(t, 360, ff.capturedVideoHLS.Resolutions[0].Height)
	require.Contains(t, storageClient.uploads, job.GetThumbnailOutput().GetObjectKey())
	require.Contains(t, storageClient.uploads, job.GetHlsOutput().GetObjectPrefix()+"/master.m3u8")
	require.Equal(t, job.GetEventId(), pub.complete.GetEventId())
}

func TestProcessVideoJobFailsWhenAllocatedThumbnailGenerationFails(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{
		workDir:       workDir,
		thumbnailErrs: []error{errors.New("webp failed"), nil},
	}
	storageClient := &recordingCleanupStorage{}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t, &config.Config{JobTimeoutMins: 5}, ff, storageClient, pub)

	job := videoJob("video-job-2", "work-1", "video-file-2")
	job.EntityType = apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK
	job.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: true}

	err := h.HandleVideoJob(context.Background(), job)
	require.Error(t, err)
	require.Contains(t, err.Error(), "video thumbnail failed")
	require.Empty(t, storageClient.uploads)
	require.NotNil(t, pub.complete)
	require.False(t, pub.complete.Success)
	require.Equal(t, job.GetEventId(), pub.complete.GetEventId())
}

func TestProcessVideoJobPublishesFailureWhenValidationFails(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	ff := &fakeHandlerFFmpeg{workDir: workDir}
	pub := &recordingHandlerPublisher{}
	h := newTestHandler(t, &config.Config{JobTimeoutMins: 5}, ff, fakeHandlerStorage{uploadErr: errors.New("upload failed")}, pub)

	job := videoJob("video-job-3", "post-1", "video-file-3")
	job.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: true}

	err := h.HandleVideoJob(context.Background(), job)
	require.Error(t, err)
	require.Contains(t, err.Error(), "video thumbnail upload failed")
	require.NotNil(t, pub.complete)
	require.False(t, pub.complete.Success)
	require.NotNil(t, pub.complete.Error)
	require.Equal(t, job.GetEventId(), pub.complete.GetEventId())
}

func TestHandlerHelpersCoverEnumsAndCancellation(t *testing.T) {
	t.Parallel()

	h := newTestHandler(t, &config.Config{}, nil, nil, nil)
	first, started := h.jobs.Start(context.Background(), "event-1", "file-1")
	require.True(t, started)
	duplicate, started := h.jobs.Start(context.Background(), "event-1", "file-1")
	require.False(t, started)
	require.Nil(t, duplicate)
	second, started := h.jobs.Start(context.Background(), "event-2", "file-1")
	require.True(t, started)
	require.True(t, h.CancelJob("file-1"))
	require.ErrorIs(t, first.Context.Err(), context.Canceled)
	require.ErrorIs(t, second.Context.Err(), context.Canceled)
	require.True(t, h.CancelJob("file-1"))
	first.Close()
	second.Close()
	require.False(t, h.CancelJob("file-1"))
	resolutions := videoResolutionsToFFmpeg([]apiv1.VideoResolution{
		apiv1.VideoResolution_VIDEO_RESOLUTION_360P,
		apiv1.VideoResolution_VIDEO_RESOLUTION_480P,
		apiv1.VideoResolution_VIDEO_RESOLUTION_720P,
		apiv1.VideoResolution_VIDEO_RESOLUTION_1080P,
		apiv1.VideoResolution_VIDEO_RESOLUTION_UNSPECIFIED,
	})
	require.Equal(t, []int{360, 480, 720, 1080}, []int{resolutions[0].Height, resolutions[1].Height, resolutions[2].Height, resolutions[3].Height})

	_, err := assetWriteResult(&commonv1.AssetWriteTarget{}, t.TempDir())
	require.Error(t, err)
	_, err = fileSHA256(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestProgressReporterRateLimitsAndRejectsNonIncreasingProgress(t *testing.T) {
	t.Parallel()
	pub := &recordingHandlerPublisher{}
	report := (progressPublisher{publisher: pub}).newReporter(context.Background(), "job", apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "entity", "file")
	stage := apiv1.TranscodeStage_TRANSCODE_STAGE_PROCESSING
	report(stage, 10)
	report(stage, 20)
	require.Len(t, pub.progress, 1)
	time.Sleep(510 * time.Millisecond)
	report(stage, 5)
	require.Len(t, pub.progress, 1)
	report(stage, 30)
	require.Len(t, pub.progress, 2)
}

func TestFailurePublishesTerminalResultWithoutStorageMutation(t *testing.T) {
	job := audioJob("retry-job", "entity", "file")
	storage := &recordingCleanupStorage{}
	publisher := &recordingHandlerPublisher{}
	h := newTestHandler(t, &config.Config{}, nil, storage, publisher)
	err := h.audio.fail(
		context.Background(),
		job,
		time.Now(),
		errors.New("processing failed"),
	)
	require.True(t, jobresult.IsTerminal(err))
	require.Empty(t, storage.uploads)
}

func TestFailureResultPublicationIsRetryable(t *testing.T) {
	publisher := &recordingHandlerPublisher{completeErr: errors.New("result unavailable")}
	h := newTestHandler(t, &config.Config{}, nil, &recordingCleanupStorage{}, publisher)
	require.True(t, jobresult.IsRetry(h.audio.fail(context.Background(), audioJob("audio-fail-result", "entity", "file"), time.Now(), errors.New("failed"))))
	require.True(t, jobresult.IsRetry(h.video.fail(context.Background(), videoJob("video-fail-result", "entity", "file"), time.Now(), errors.New("failed"))))
}

func TestHandlerParsesProtobufJobs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		job  proto.Message
		run  func(*Handler) mq.Handler
	}{
		{name: "audio", job: audioJob("parse-audio", "entity", "audio"), run: func(h *Handler) mq.Handler { return mq.DecodeAudioJob(h.HandleAudioJob) }},
		{name: "video", job: videoJob("parse-video", "entity", "video"), run: func(h *Handler) mq.Handler { return mq.DecodeVideoJob(h.HandleVideoJob) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			h := newTestHandler(t, &config.Config{AudioHLSBitrate: "128k", JobTimeoutMins: 5}, &fakeHandlerFFmpeg{workDir: workDir}, fakeHandlerStorage{}, &recordingHandlerPublisher{})
			body, err := proto.Marshal(tc.job)
			require.NoError(t, err)
			run := tc.run(h)
			require.NoError(t, run(context.Background(), body))
			require.ErrorContains(t, run(context.Background(), []byte("invalid")), "failed to parse")
		})
	}
}

func TestProcessAudioJobFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ffmpeg  func(string) *fakeHandlerFFmpeg
		storage fakeHandlerStorage
		pub     *recordingHandlerPublisher
		want    string
		retry   bool
	}{
		{name: "work dir", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, createErr: errors.New("mkdir")}
		}, pub: &recordingHandlerPublisher{}, want: "mkdir"},
		{name: "download", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{downloadErr: errors.New("download")}, pub: &recordingHandlerPublisher{}, want: "download failed"},
		{name: "validation", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, validateAudioErr: errors.New("invalid audio")}
		}, pub: &recordingHandlerPublisher{}, want: "invalid audio"},
		{name: "probe", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, probeErr: errors.New("probe")}
		}, pub: &recordingHandlerPublisher{}, want: "probe failed"},
		{name: "spectrogram", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, spectrogramErr: errors.New("spectrogram")}
		}, pub: &recordingHandlerPublisher{}, want: "audio spectrogram failed"},
		{name: "spectrogram upload", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{uploadErr: errors.New("upload"), uploadErrMatch: "asset/"}, pub: &recordingHandlerPublisher{}, want: "spectrogram upload failed"},
		{name: "spectrogram stat", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{removeOnUpload: true}, pub: &recordingHandlerPublisher{}, want: "stat spectrogram output"},
		{name: "hls generation", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, audioHLSErr: errors.New("hls")}
		}, pub: &recordingHandlerPublisher{}, want: "audio hls failed"},
		{name: "hls validation", ffmpeg: func(dir string) *fakeHandlerFFmpeg { return &fakeHandlerFFmpeg{workDir: dir, invalidAudioHLS: true} }, pub: &recordingHandlerPublisher{}, want: "validate audio hls output"},
		{name: "hls upload", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{uploadErr: errors.New("upload"), uploadErrMatch: ".ts"}, pub: &recordingHandlerPublisher{}, want: "audio hls upload failed"},
		{name: "completion", ffmpeg: newFakeHandlerFFmpeg, pub: &recordingHandlerPublisher{completeErr: errors.New("publish")}, want: "publish audio completion", retry: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			ff := tc.ffmpeg(workDir)
			storage := tc.storage
			pub := tc.pub
			h := newTestHandler(t, &config.Config{AudioHLSBitrate: "128k", JobTimeoutMins: 5}, ff, storage, pub)
			err := h.HandleAudioJob(context.Background(), audioJob("failure-"+tc.name, "entity", "audio-"+tc.name))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, tc.retry, jobresult.IsRetry(err))
			if tc.name != "completion" {
				require.NotNil(t, pub.complete)
				require.False(t, pub.complete.GetSuccess())
			}
		})
	}
}

func TestProcessVideoJobFailureBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ffmpeg  func(string) *fakeHandlerFFmpeg
		storage fakeHandlerStorage
		pub     *recordingHandlerPublisher
		want    string
		retry   bool
	}{
		{name: "work dir", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, createErr: errors.New("mkdir")}
		}, pub: &recordingHandlerPublisher{}, want: "mkdir"},
		{name: "download", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{downloadErr: errors.New("download")}, pub: &recordingHandlerPublisher{}, want: "download failed"},
		{name: "validation", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, validateVideoErr: errors.New("invalid video")}
		}, pub: &recordingHandlerPublisher{}, want: "invalid video"},
		{name: "probe", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, probeErr: errors.New("probe")}
		}, pub: &recordingHandlerPublisher{}, want: "probe failed"},
		{name: "thumbnail stat", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{removeOnUpload: true}, pub: &recordingHandlerPublisher{}, want: "stat thumbnail output"},
		{name: "hls generation", ffmpeg: func(dir string) *fakeHandlerFFmpeg {
			return &fakeHandlerFFmpeg{workDir: dir, videoHLSErr: errors.New("hls")}
		}, pub: &recordingHandlerPublisher{}, want: "video hls failed"},
		{name: "hls validation", ffmpeg: func(dir string) *fakeHandlerFFmpeg { return &fakeHandlerFFmpeg{workDir: dir, invalidVideoHLS: true} }, pub: &recordingHandlerPublisher{}, want: "validate video hls output"},
		{name: "hls upload", ffmpeg: newFakeHandlerFFmpeg, storage: fakeHandlerStorage{uploadErr: errors.New("upload"), uploadErrMatch: ".ts"}, pub: &recordingHandlerPublisher{}, want: "video hls upload failed"},
		{name: "completion", ffmpeg: newFakeHandlerFFmpeg, pub: &recordingHandlerPublisher{completeErr: errors.New("publish")}, want: "publish video completion", retry: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			ff := tc.ffmpeg(workDir)
			pub := tc.pub
			h := newTestHandler(t, &config.Config{JobTimeoutMins: 5}, ff, tc.storage, pub)
			job := videoJob("failure-"+tc.name, "entity", "video-"+tc.name)
			job.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: true}
			err := h.HandleVideoJob(context.Background(), job)
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, tc.retry, jobresult.IsRetry(err))
			if tc.name != "completion" {
				require.NotNil(t, pub.complete)
				require.False(t, pub.complete.GetSuccess())
			}
		})
	}
}

func TestCompletionPublishUncertaintyPreservesAllocatedOutputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(*Handler) error
	}{
		{
			name: "audio",
			run: func(handler *Handler) error {
				return handler.HandleAudioJob(context.Background(), audioJob("uncertain-audio", "entity", "audio"))
			},
		},
		{
			name: "video",
			run: func(handler *Handler) error {
				job := videoJob("uncertain-video", "entity", "video")
				job.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: true}
				return handler.HandleVideoJob(context.Background(), job)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &recordingCleanupStorage{}
			handler := newTestHandler(t,
				&config.Config{AudioHLSBitrate: "128k", JobTimeoutMins: 5},
				newFakeHandlerFFmpeg(t.TempDir()),
				storage,
				&recordingHandlerPublisher{completeErr: errors.New("confirmation uncertain")},
			)

			err := tc.run(handler)
			require.ErrorContains(t, err, "completion")
			require.True(t, jobresult.IsRetry(err))
			require.NotEmpty(t, storage.uploads)
		})
	}
}

func TestFreshDuplicateReplaysAcceptedCompletionWithoutTouchingOutputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  transcodeCommand
		run  func(*Handler, transcodeCommand) error
	}{
		{
			name: "audio",
			job:  audioJob("fresh-duplicate-audio", "entity", "audio"),
			run: func(handler *Handler, command transcodeCommand) error {
				return handler.HandleAudioJob(context.Background(), command.(*apiv1.TranscodeAudioEvent))
			},
		},
		{
			name: "video",
			job: func() *apiv1.TranscodeVideoEvent {
				job := videoJob("fresh-duplicate-video", "entity", "video")
				job.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: true}
				return job
			}(),
			run: func(handler *Handler, command transcodeCommand) error {
				return handler.HandleVideoJob(context.Background(), command.(*apiv1.TranscodeVideoEvent))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &recordingCleanupStorage{}
			publisher := &recordingHandlerPublisher{completeErr: errors.New("confirmation uncertain")}
			first := newTestHandler(t,
				&config.Config{AudioHLSBitrate: "128k", JobTimeoutMins: 5},
				newFakeHandlerFFmpeg(t.TempDir()),
				storage,
				publisher,
			)
			err := tc.run(first, tc.job)
			require.Error(t, err)
			require.True(t, jobresult.IsRetry(err))
			acceptedUploads := append([]string(nil), storage.uploads...)
			require.NotEmpty(t, acceptedUploads)

			publisher.completeErr = nil
			publisher.complete = nil
			fresh := newTestHandler(t,
				&config.Config{AudioHLSBitrate: "128k", JobTimeoutMins: 5},
				&fakeHandlerFFmpeg{workDir: t.TempDir(), createErr: errors.New("duplicate must not reprocess")},
				storage,
				publisher,
			)
			require.NoError(t, tc.run(fresh, tc.job))
			require.Equal(t, acceptedUploads, storage.uploads)
			require.NotNil(t, publisher.complete)
			require.True(t, publisher.complete.GetSuccess())
			require.Equal(t, tc.job.GetEventId(), publisher.complete.GetEventId())
		})
	}
}

func TestTranscodeCompletionReplayRejectsUnavailableOrInvalidReceipts(t *testing.T) {
	job := audioJob("receipt-boundary", "entity", "audio")
	key := job.GetHlsOutput().GetObjectPrefix() + "/" + hls.MasterManifestName

	storage := &recordingCleanupStorage{completionErr: errors.New("head unavailable")}
	h := newTestHandler(t, &config.Config{}, nil, storage, &recordingHandlerPublisher{})
	replayed, err := h.audio.completions.replay(context.Background(), job, key, audioCompletionExpectation(job))
	require.False(t, replayed)
	require.True(t, jobresult.IsRetry(err))

	storage.completionErr = nil
	storage.completed = map[string][]byte{key: []byte("invalid")}
	replayed, err = h.audio.completions.replay(context.Background(), job, key, audioCompletionExpectation(job))
	require.False(t, replayed)
	require.ErrorContains(t, err, "decode transcode completion")

	mismatch, marshalErr := proto.Marshal(&apiv1.TranscodeCompleteEvent{EventId: testUUID("other"), Success: true})
	require.NoError(t, marshalErr)
	storage.completed[key] = mismatch
	replayed, err = h.audio.completions.replay(context.Background(), job, key, audioCompletionExpectation(job))
	require.False(t, replayed)
	require.ErrorContains(t, err, "does not match command")

	accepted := &recordingHandlerPublisher{completeErr: errors.New("result unavailable")}
	storage.completed[key], marshalErr = proto.Marshal(&apiv1.TranscodeCompleteEvent{
		EventId:    job.GetEventId(),
		EventType:  apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.GetEntityType(),
		EntityId:   job.GetEntityId(),
		FileId:     job.GetFileId(),
		Success:    true,
		Outputs: &apiv1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: job.GetHlsOutput().GetGenerationId()},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: job.GetSpectrogramOutput().GetAssetId()},
		},
	})
	require.NoError(t, marshalErr)
	h.audio.completions.publisher = accepted
	replayed, err = h.audio.completions.replay(context.Background(), job, key, audioCompletionExpectation(job))
	require.False(t, replayed)
	require.True(t, jobresult.IsRetry(err))
}

func TestProcessJobsRetryWhenCompletionLookupIsUnavailable(t *testing.T) {
	storage := &recordingCleanupStorage{completionErr: errors.New("head unavailable")}
	h := newTestHandler(t, &config.Config{}, &fakeHandlerFFmpeg{createErr: errors.New("must not process")}, storage, &recordingHandlerPublisher{})
	require.True(t, jobresult.IsRetry(h.HandleAudioJob(context.Background(), audioJob("audio-lookup", "entity", "audio"))))
	require.True(t, jobresult.IsRetry(h.HandleVideoJob(context.Background(), videoJob("video-lookup", "entity", "video"))))
}

func TestProcessJobsCoverOptionalSettingsAndIdempotentRetry(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	ff := newFakeHandlerFFmpeg(workDir)
	storage := &recordingCleanupStorage{}
	pub := &recordingHandlerPublisher{progressErr: errors.New("ignored progress failure")}
	h := newTestHandler(t, &config.Config{AudioHLSBitrate: "96k", JobTimeoutMins: 5}, ff, storage, pub)

	audio := audioJob("retry-audio", "entity", "audio")
	require.NoError(t, h.HandleAudioJob(context.Background(), audio))
	firstUploads := append([]string(nil), storage.uploads...)
	require.NoError(t, h.HandleAudioJob(context.Background(), audio))
	require.Equal(t, firstUploads, storage.uploads, "durable completion should skip repeated processing")
	require.Equal(t, int32(42), pub.complete.GetOutputs().GetDurationSeconds())

	video := videoJob("video-no-thumbnail", "entity", "video")
	video.Options = &apiv1.VideoTranscodeOptions{
		GenerateThumbnail: false,
		Resolutions:       []apiv1.VideoResolution{apiv1.VideoResolution_VIDEO_RESOLUTION_UNSPECIFIED},
	}
	video.ThumbnailOutput = nil
	require.NoError(t, h.HandleVideoJob(context.Background(), video))
	require.Nil(t, pub.complete.GetOutputs().GetThumbnail())
}

func TestProcessJobsIgnoreConcurrentDuplicateTargets(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t, &config.Config{}, nil, nil, nil)

	audio := audioJob("duplicate-audio", "entity", "audio")
	audioSession, started := h.jobs.Start(context.Background(), audio.GetEventId(), audio.GetFileId())
	require.True(t, started)
	require.NoError(t, h.HandleAudioJob(context.Background(), audio))
	audioSession.Close()

	video := videoJob("duplicate-video", "entity", "video")
	videoSession, started := h.jobs.Start(context.Background(), video.GetEventId(), video.GetFileId())
	require.True(t, started)
	require.NoError(t, h.HandleVideoJob(context.Background(), video))
	videoSession.Close()
}

func newFakeHandlerFFmpeg(workDir string) *fakeHandlerFFmpeg {
	return &fakeHandlerFFmpeg{workDir: workDir}
}

func newTestHandler(
	t *testing.T,
	cfg *config.Config,
	executor FFmpegExecutor,
	storage StorageClient,
	publisher EventPublisher,
) *Handler {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.JobTimeoutMins <= 0 {
		cfg.JobTimeoutMins = 5
	}
	if cfg.AudioHLSBitrate == "" {
		cfg.AudioHLSBitrate = "128k"
	}
	if executor == nil {
		executor = &fakeHandlerFFmpeg{workDir: t.TempDir()}
	}
	if storage == nil {
		storage = fakeHandlerStorage{}
	}
	if publisher == nil {
		publisher = &recordingHandlerPublisher{}
	}
	handler, err := NewHandler(Options{
		JobTimeoutMinutes: cfg.JobTimeoutMins,
		AudioHLSBitrate:   cfg.AudioHLSBitrate,
		FFmpeg:            executor,
		Storage:           storage,
		Publisher:         publisher,
	})
	require.NoError(t, err)
	return handler
}
