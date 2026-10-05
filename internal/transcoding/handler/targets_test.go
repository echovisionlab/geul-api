package handler

import (
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestValidateCanonicalJobs(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateAudioJob(audioJob("event", "entity", "audio")))
	require.NoError(t, validateVideoJob(videoJob("event", "entity", "video")))

	withoutThumbnail := videoJob("event-no-thumb", "entity", "video-no-thumb")
	withoutThumbnail.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: false}
	withoutThumbnail.ThumbnailOutput = nil
	require.NoError(t, validateVideoJob(withoutThumbnail))
}

type audioValidationCase struct {
	name   string
	mutate func(*apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent
	want   string
}

func TestValidateAudioJobRejectsInvalidSourceContracts(t *testing.T) {
	t.Parallel()
	assertInvalidAudioJobs(t, []audioValidationCase{
		{name: "nil", mutate: func(*apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { return nil }, want: "audio job is required"},
		{name: "event id", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { job.EventId = "bad"; return job }, want: "event_id must be a canonical UUID"},
		{name: "entity id", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { job.EntityId = "bad"; return job }, want: "entity_id must be a canonical UUID"},
		{name: "file id", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { job.FileId = "bad"; return job }, want: "file_id must be a canonical UUID"},
		{name: "entity type", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.EntityType = apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
			return job
		}, want: "unsupported entity_type"},
		{name: "source missing", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { job.Source = nil; return job }, want: "target is required"},
		{name: "source file", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.FileId = testUUID("other")
			return job
		}, want: "does not match"},
		{name: "source extension", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.Extension = ".mp3"
			return job
		}, want: "extension is not canonical"},
		{name: "source uppercase extension", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.Extension = "MP3"
			return job
		}, want: "extension is not canonical"},
		{name: "source key", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.ObjectKey = "media/wrong.mp3"
			return job
		}, want: "object_key must be"},
		{name: "source mime", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.MimeType = "video/mp4"
			return job
		}, want: "must start with"},
		{name: "source parameterized mime", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.MimeType = "audio/mpeg; charset=binary"
			return job
		}, want: "mime_type is not canonical"},
		{name: "source mime mismatch", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.MimeType = "audio/ogg"
			return job
		}, want: "extension does not match"},
		{name: "source unknown mime", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.Source.MimeType = "audio/unknown"
			return job
		}, want: "extension does not match"},
	})
}

func TestValidateAudioJobRejectsInvalidOutputContracts(t *testing.T) {
	t.Parallel()
	assertInvalidAudioJobs(t, []audioValidationCase{
		{name: "hls missing", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent { job.HlsOutput = nil; return job }, want: "target is required"},
		{name: "hls file", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.HlsOutput.FileId = testUUID("other")
			return job
		}, want: "does not match"},
		{name: "hls generation", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.HlsOutput.GenerationId = "bad"
			return job
		}, want: "invalid media path"},
		{name: "hls prefix", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.HlsOutput.ObjectPrefix += "/"
			return job
		}, want: "object_prefix must be"},
		{name: "asset missing", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput = nil
			return job
		}, want: "target is required"},
		{name: "asset id", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput.AssetId = "bad"
			return job
		}, want: "invalid media path"},
		{name: "asset extension", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput.Extension = "webp"
			return job
		}, want: "extension must be"},
		{name: "asset mime", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput.MimeType = "image/webp"
			return job
		}, want: "mime_type must be"},
		{name: "asset key", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput.ObjectKey = "asset/wrong.png"
			return job
		}, want: "object_key must be"},
		{name: "asset disposition", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			job.SpectrogramOutput.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
			return job
		}, want: "inline disposition"},
		{name: "asset filename", mutate: func(job *apiv1.TranscodeAudioEvent) *apiv1.TranscodeAudioEvent {
			name := "spectrogram.png"
			job.SpectrogramOutput.DownloadFilename = &name
			return job
		}, want: "download filename"},
	})
}

func assertInvalidAudioJobs(t *testing.T, tests []audioValidationCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := audioJob("event", "entity", "audio")
			require.ErrorContains(t, validateAudioJob(tc.mutate(base)), tc.want)
		})
	}
}

func TestValidateVideoJobRejectsInvalidContracts(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, validateVideoJob(nil), "video job is required")
	h := newTestHandler(t, nil, nil, nil, nil)
	require.ErrorContains(t, h.HandleAudioJob(t.Context(), nil), "audio job is required")
	require.ErrorContains(t, h.HandleVideoJob(t.Context(), nil), "video job is required")

	wrongMime := videoJob("event", "entity", "video")
	wrongMime.Source.MimeType = "audio/mpeg"
	require.ErrorContains(t, validateVideoJob(wrongMime), "invalid video source")

	badIdentity := videoJob("event-bad-id", "entity", "video")
	badIdentity.EventId = "bad"
	require.ErrorContains(t, validateVideoJob(badIdentity), "event_id must be a canonical UUID")

	badHLS := videoJob("event-bad-hls", "entity", "video")
	badHLS.HlsOutput.ObjectPrefix += "/"
	require.ErrorContains(t, validateVideoJob(badHLS), "invalid video HLS target")

	missingThumbnail := videoJob("event-missing", "entity", "video")
	missingThumbnail.ThumbnailOutput = nil
	require.ErrorContains(t, validateVideoJob(missingThumbnail), "invalid thumbnail target")

	disabledWithTarget := videoJob("event-disabled", "entity", "video")
	disabledWithTarget.Options = &apiv1.VideoTranscodeOptions{GenerateThumbnail: false}
	require.ErrorContains(t, validateVideoJob(disabledWithTarget), "must be omitted")

	badThumbnail := videoJob("event-bad-thumb", "entity", "video")
	badThumbnail.ThumbnailOutput = &commonv1.AssetWriteTarget{}
	require.ErrorContains(t, validateVideoJob(badThumbnail), "invalid thumbnail target")
}
