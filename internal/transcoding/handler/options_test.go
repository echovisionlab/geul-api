package handler

import (
	"context"
	"testing"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestNewHandlerRejectsIncompleteOptions(t *testing.T) {
	t.Parallel()
	valid := Options{
		JobTimeoutMinutes: 5,
		AudioHLSBitrate:   "128k",
		FFmpeg:            &fakeHandlerFFmpeg{workDir: t.TempDir()},
		Storage:           fakeHandlerStorage{},
		Publisher:         &recordingHandlerPublisher{},
	}
	invalid := []Options{
		{},
		func() Options { options := valid; options.AudioHLSBitrate = ""; return options }(),
		func() Options { options := valid; options.FFmpeg = nil; return options }(),
		func() Options { options := valid; options.Storage = nil; return options }(),
		func() Options { options := valid; options.Publisher = nil; return options }(),
	}
	for _, options := range invalid {
		handler, err := NewHandler(options)
		require.Error(t, err)
		require.Nil(t, handler)
	}
}

func TestCompletionEncodingAndCleanupErrorsAreHandled(t *testing.T) {
	t.Parallel()
	executor := &fakeHandlerFFmpeg{workDir: t.TempDir(), cleanupErr: context.Canceled}
	handler := newTestHandler(t, nil, executor, nil, nil)
	require.NoError(t, handler.HandleAudioJob(context.Background(), audioJob("cleanup", "entity", "file")))

	complete := &apiv1.TranscodeCompleteEvent{
		EventId: "\xff",
		Outputs: &apiv1.TranscodeOutputs{},
	}
	report := func(apiv1.TranscodeStage, int) {}
	err := handler.audio.completions.uploadHLS(
		context.Background(),
		audioJob("encoding", "entity", "file").GetHlsOutput(),
		func() string {
			dir := t.TempDir()
			require.NoError(t, writeAudioHLSPackage(dir))
			return dir
		}(),
		complete,
		report,
		"audio",
	)
	require.ErrorContains(t, err, "encode audio completion")
}
