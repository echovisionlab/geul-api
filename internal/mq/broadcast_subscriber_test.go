package mq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/telemetry"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestBroadcastSubscriberStartStopsWhenReconnectSeesCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	closed := make(chan struct{})
	close(closed)
	subscriber := &BroadcastSubscriber{closed: closed}
	done := make(chan error, 1)
	go func() {
		done <- subscriber.Start(ctx)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancelled subscriber did not stop")
	}
}

func TestBroadcastReconnectDelayIsBounded(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 250 * time.Millisecond},
		{attempt: 1, want: 250 * time.Millisecond},
		{attempt: 19, want: 4750 * time.Millisecond},
		{attempt: 20, want: 5 * time.Second},
		{attempt: 1_000_000, want: 5 * time.Second},
	}

	for _, test := range tests {
		require.Equal(t, test.want, broadcastReconnectDelay(test.attempt))
	}
}

func TestBroadcastConnectFailureLogsSanitizedCauseAndSignal(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	subscriber, err := NewBroadcastSubscriber("postgres://private-user:private-password@%zz/db", "test_signal", func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, subscriber.Start(ctx))
	require.Contains(t, output.String(), "PostgreSQL signal listener connect failed")
	require.Contains(t, output.String(), "test_signal")
	require.Contains(t, output.String(), "failed to parse as URL")
	require.NotContains(t, output.String(), "private-user")
	require.NotContains(t, output.String(), "private-password")
}

func TestBroadcastHandlerFailureKeepsMessageCorrelation(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := errors.New("refresh adapter: database unavailable for person@example.test")
	subscriber, err := NewBroadcastSubscriber("unused", "test_signal", func(context.Context, []byte) error { return cause })
	require.NoError(t, err)
	envelope, err := eventpkg.NewEnvelope("message-1", "api.test.Message", nil)
	require.NoError(t, err)
	envelope.CorrelationID = "command-1"
	payload, err := json.Marshal(envelope)
	require.NoError(t, err)
	require.ErrorIs(t, subscriber.process(t.Context(), string(payload)), cause)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "test_signal", entry["signal"])
	require.Equal(t, "message-1", entry["message_id"])
	require.Equal(t, "command-1", entry["command_id"])
	require.Contains(t, entry["failure_cause"], "refresh adapter: database unavailable")
	require.NotContains(t, output.String(), "person@example.test")
}
