package mq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type subscriberWaitResult struct {
	notification *pgconn.Notification
	err          error
}

type controlledSignalConnection struct {
	waits     chan subscriberWaitResult
	ready     chan struct{}
	closed    chan struct{}
	readyOnce sync.Once
	closeOnce sync.Once
}

func newControlledSignalConnection() *controlledSignalConnection {
	return &controlledSignalConnection{
		waits:  make(chan subscriberWaitResult, 2),
		ready:  make(chan struct{}),
		closed: make(chan struct{}),
	}
}

func (c *controlledSignalConnection) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	c.readyOnce.Do(func() { close(c.ready) })
	return pgconn.CommandTag{}, nil
}

func (c *controlledSignalConnection) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case result := <-c.waits:
		return result.notification, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *controlledSignalConnection) Close(context.Context) error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

type notifyingLogHandler struct {
	slog.Handler
	notified chan struct{}
	once     sync.Once
}

func (h *notifyingLogHandler) Handle(ctx context.Context, record slog.Record) error {
	h.once.Do(func() { close(h.notified) })
	return h.Handler.Handle(ctx, record)
}

func receiveBeforeDeadline[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case value := <-values:
		return value
	case <-timer.C:
		t.Fatal("timed out waiting for subscriber lifecycle event")
		var zero T
		return zero
	}
}

func TestCancelSubscriberReconnectsAndStopsCleanly(t *testing.T) {
	originalSignal := connectSignal
	t.Cleanup(func() { connectSignal = originalSignal })

	first := newControlledSignalConnection()
	second := newControlledSignalConnection()
	connections := make(chan signalConnection, 2)
	connections <- first
	connections <- second
	connectSignal = func(ctx context.Context, _ string) (signalConnection, error) {
		select {
		case connection := <-connections:
			return connection, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	first.waits <- subscriberWaitResult{notification: &pgconn.Notification{Payload: string(cancelEnvelope(
		t,
		&apiv1.TranscodeCancelEvent{FileId: "file-to-cancel"},
		"cancel-message",
		"api.manage.v1.TranscodeCancelEvent",
	))}}
	received := make(chan string, 1)
	subscriber, err := NewCancelSubscriber(&Connection{dsn: "test"}, func(id string) bool {
		received <- id
		return true
	})
	require.NoError(t, err)
	require.Equal(t, "file-to-cancel", receiveBeforeDeadline(t, received))
	require.True(t, subscriber.Healthy())
	require.False(t, subscriber.subscriber.IsClosed())
	require.True(t, subscriber.subscriber.Healthy())

	first.waits <- subscriberWaitResult{err: errors.New("listener interrupted")}
	receiveBeforeDeadline(t, second.ready)
	require.True(t, subscriber.Healthy())

	require.NoError(t, subscriber.Close())
	receiveBeforeDeadline(t, second.closed)
	require.False(t, subscriber.Healthy())
	require.True(t, subscriber.subscriber.IsClosed())
	require.False(t, subscriber.subscriber.Healthy())
	require.NoError(t, subscriber.Close())
}

func TestCancelSubscriberCloseInterruptsReconnectBackoff(t *testing.T) {
	originalSignal := connectSignal
	originalLogger := slog.Default()
	t.Cleanup(func() {
		connectSignal = originalSignal
		slog.SetDefault(originalLogger)
	})

	logged := make(chan struct{})
	slog.SetDefault(slog.New(&notifyingLogHandler{
		Handler:  slog.NewTextHandler(io.Discard, nil),
		notified: logged,
	}))
	connection := newControlledSignalConnection()
	connectSignal = func(context.Context, string) (signalConnection, error) {
		return connection, nil
	}

	subscriber, err := newCancelSubscriber(
		&Connection{dsn: "test"},
		eventpkg.SignalTranscodeCancel,
		decodeTranscodeCancel,
		func(string) bool { return true },
	)
	require.NoError(t, err)
	connection.waits <- subscriberWaitResult{err: errors.New("listener interrupted")}
	receiveBeforeDeadline(t, logged)
	require.NoError(t, subscriber.Close())
	require.True(t, subscriber.IsClosed())
}

func TestWaveformCancelSubscriberLifecycleAndNilHealth(t *testing.T) {
	originalSignal := connectSignal
	t.Cleanup(func() { connectSignal = originalSignal })

	connection := newControlledSignalConnection()
	connectSignal = func(context.Context, string) (signalConnection, error) {
		return connection, nil
	}

	subscriber, err := NewWaveformCancelSubscriber(&Connection{dsn: "test"}, func(string) bool { return true })
	require.NoError(t, err)
	require.True(t, subscriber.Healthy())
	require.NoError(t, subscriber.Close())
	require.False(t, subscriber.Healthy())

	require.True(t, (*cancelSubscriber)(nil).IsClosed())
	require.False(t, (*cancelSubscriber)(nil).Healthy())
	require.False(t, (*CancelSubscriber)(nil).Healthy())
	require.False(t, (*WaveformCancelSubscriber)(nil).Healthy())
}
