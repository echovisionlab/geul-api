package mq

import (
	"context"
	"testing"
	"time"

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
