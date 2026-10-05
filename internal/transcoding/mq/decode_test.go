package mq

import (
	"context"
	"testing"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTypedJobDecoders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	audioBody, err := proto.Marshal(&apiv1.TranscodeAudioEvent{EventId: "audio"})
	require.NoError(t, err)
	audioCalled := false
	audio := DecodeAudioJob(func(_ context.Context, event *apiv1.TranscodeAudioEvent) error {
		audioCalled = event.GetEventId() == "audio"
		return nil
	})
	require.NoError(t, audio(ctx, audioBody))
	require.True(t, audioCalled)
	require.ErrorContains(t, audio(ctx, []byte("invalid")), "parse audio")

	videoBody, err := proto.Marshal(&apiv1.TranscodeVideoEvent{EventId: "video"})
	require.NoError(t, err)
	videoCalled := false
	video := DecodeVideoJob(func(_ context.Context, event *apiv1.TranscodeVideoEvent) error {
		videoCalled = event.GetEventId() == "video"
		return nil
	})
	require.NoError(t, video(ctx, videoBody))
	require.True(t, videoCalled)
	require.ErrorContains(t, video(ctx, []byte("invalid")), "parse video")

	waveformBody, err := proto.Marshal(&apiv1.WaveformGenerateEvent{EventId: "waveform"})
	require.NoError(t, err)
	waveformCalled := false
	waveform := DecodeWaveformJob(func(_ context.Context, event *apiv1.WaveformGenerateEvent) error {
		waveformCalled = event.GetEventId() == "waveform"
		return nil
	})
	require.NoError(t, waveform(ctx, waveformBody))
	require.True(t, waveformCalled)
	require.ErrorContains(t, waveform(ctx, []byte("invalid")), "parse waveform")
}
