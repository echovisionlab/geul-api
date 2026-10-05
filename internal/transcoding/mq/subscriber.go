package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

// CancelHandler cancels active transcoding for a file and reports whether a job was found.
type CancelHandler func(fileID string) bool

// WaveformCancelHandler cancels an active waveform job and reports whether it was found.
type WaveformCancelHandler func(eventID string) bool

type cancelSubscriber struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	ready  atomic.Bool
}

// CancelSubscriber listens for transcode cancellation signals.
type CancelSubscriber struct{ subscriber *cancelSubscriber }

// WaveformCancelSubscriber listens for waveform cancellation signals.
type WaveformCancelSubscriber struct{ subscriber *cancelSubscriber }

func newCancelSubscriber(
	conn *Connection,
	signal string,
	decode func([]byte) (string, error),
	handler func(string) bool,
) (*cancelSubscriber, error) {
	if conn == nil || decode == nil || handler == nil {
		return nil, fmt.Errorf("connection, decoder, and handler are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	subscriber := &cancelSubscriber{cancel: cancel, done: make(chan struct{})}
	startup := make(chan error, 1)
	go func() {
		defer close(subscriber.done)
		firstAttempt := true
		for ctx.Err() == nil {
			becameReady := false
			err := conn.listen(ctx, signal, func() {
				becameReady = true
				subscriber.ready.Store(true)
				if firstAttempt {
					startup <- nil
					firstAttempt = false
				}
			}, func(payload []byte) { handleCancelSignal(payload, decode, handler) })
			subscriber.ready.Store(false)
			if firstAttempt {
				startup <- err
				firstAttempt = false
				return
			}
			if ctx.Err() != nil {
				return
			}
			slog.Error("PostgreSQL signal listener stopped; reconnecting", "signal", signal, "ready", becameReady, "error", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	if err := <-startup; err != nil {
		cancel()
		<-subscriber.done
		return nil, err
	}
	return subscriber, nil
}

func handleCancelSignal(payload []byte, decode func([]byte) (string, error), handler func(string) bool) {
	var envelope eventpkg.Envelope
	if json.Unmarshal(payload, &envelope) != nil {
		return
	}
	body, err := envelope.Payload()
	if err != nil {
		return
	}
	id, err := decode(body)
	if err == nil {
		handler(id)
	}
}

// NewCancelSubscriber creates and starts a transcode cancellation subscriber.
func NewCancelSubscriber(conn *Connection, handler CancelHandler) (*CancelSubscriber, error) {
	subscriber, err := newCancelSubscriber(conn, eventpkg.SignalTranscodeCancel, decodeTranscodeCancel, handler)
	if err != nil {
		return nil, err
	}
	return &CancelSubscriber{subscriber: subscriber}, nil
}

// NewWaveformCancelSubscriber creates and starts a waveform cancellation subscriber.
func NewWaveformCancelSubscriber(conn *Connection, handler WaveformCancelHandler) (*WaveformCancelSubscriber, error) {
	subscriber, err := newCancelSubscriber(conn, eventpkg.SignalWaveformCancel, decodeWaveformCancel, handler)
	if err != nil {
		return nil, err
	}
	return &WaveformCancelSubscriber{subscriber: subscriber}, nil
}

func decodeTranscodeCancel(body []byte) (string, error) {
	var value apiv1.TranscodeCancelEvent
	if err := proto.Unmarshal(body, &value); err != nil {
		return "", err
	}
	return value.GetFileId(), nil
}

func decodeWaveformCancel(body []byte) (string, error) {
	var value apiv1.WaveformCancelEvent
	if err := proto.Unmarshal(body, &value); err != nil {
		return "", err
	}
	return value.GetEventId(), nil
}

func (s *cancelSubscriber) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(s.cancel)
	<-s.done
	return nil
}

func (s *cancelSubscriber) IsClosed() bool { return s == nil || !s.ready.Load() }
func (s *cancelSubscriber) Healthy() bool  { return !s.IsClosed() }

// Close stops the transcode cancellation subscriber.
func (s *CancelSubscriber) Close() error { return s.subscriber.Close() }

// Close stops the waveform cancellation subscriber.
func (s *WaveformCancelSubscriber) Close() error { return s.subscriber.Close() }

// Healthy reports whether the transcode cancellation subscriber is listening.
func (s *CancelSubscriber) Healthy() bool {
	return s != nil && s.subscriber.Healthy()
}

// Healthy reports whether the waveform cancellation subscriber is listening.
func (s *WaveformCancelSubscriber) Healthy() bool {
	return s != nil && s.subscriber.Healthy()
}
