package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPublishFailureReturnsJobIdentityForTheConsumerRecord(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	for _, publishErr := range []error{nil, errors.New("queue unavailable")} {
		publisher := &recordingHandlerPublisher{completeErr: publishErr}
		completions := &completionService{publisher: publisher}
		job := audioJob("event-1", "entity-1", "file-1")
		cause := errors.New("ffmpeg exited with status 1")

		err := completions.publishFailure(context.Background(), job, apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO, "audio", time.Now(), cause)

		require.ErrorContains(t, err, cause.Error())
		require.Equal(t, publishErr == nil, jobresult.IsTerminal(err))
		require.Equal(t, publishErr != nil, jobresult.IsRetry(err))
		require.Equal(t, cause.Error(), publisher.complete.GetError())
		attrs := map[string]string{}
		for _, attr := range failurecause.Of(err).Attrs {
			attrs[attr.Key] = attr.Value.String()
		}
		require.Equal(t, "audio", attrs["media_type"])
		require.Equal(t, job.GetEventId(), attrs["job_id"])
		require.Equal(t, job.GetEntityId(), attrs["entity_id"])
		require.Equal(t, job.GetFileId(), attrs["file_id"])
		require.Contains(t, attrs, "duration_ms")
	}
	// The queue consumer logs the returned failure once.
	require.Empty(t, output.String())
}
