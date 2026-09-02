package mq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunConsumerGroupCancelsSiblingsAfterTerminalError(t *testing.T) {
	terminalErr := errors.New("delivery channel closed")
	siblingStarted := make(chan struct{})
	siblingStopped := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		done <- runConsumerGroup(t.Context(), []func(context.Context) error{
			func(context.Context) error {
				<-siblingStarted
				return terminalErr
			},
			func(ctx context.Context) error {
				close(siblingStarted)
				<-ctx.Done()
				close(siblingStopped)
				return nil
			},
		})
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, terminalErr)
	case <-time.After(time.Second):
		t.Fatal("consumer group did not return after terminal error")
	}
	select {
	case <-siblingStopped:
	case <-time.After(time.Second):
		t.Fatal("sibling consumer was not cancelled")
	}
}

func TestRunConsumerGroupTreatsParentCancellationAsNormalShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runConsumerGroup(ctx, []func(context.Context) error{
		func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	})
	require.NoError(t, err)
}

func TestRunConsumerGroupReturnsAfterBoundedFatalDrainWhenSiblingIgnoresContext(t *testing.T) {
	terminalErr := errors.New("delivery channel closed")
	blocked := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- runConsumerGroupWithDrainTimeout(
			t.Context(),
			[]func(context.Context) error{
				func(context.Context) error { return terminalErr },
				func(context.Context) error {
					<-blocked
					return nil
				},
			},
			25*time.Millisecond,
		)
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, terminalErr)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("consumer group waited indefinitely for context-ignoring sibling")
	}
	close(blocked)
}

func TestRunConsumerGroupReturnsAfterBoundedParentCancellationWhenConsumerIgnoresContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	blocked := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- runConsumerGroupWithDrainTimeout(
			ctx,
			[]func(context.Context) error{
				func(context.Context) error {
					close(started)
					<-blocked
					return nil
				},
			},
			25*time.Millisecond,
		)
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("consumer group waited indefinitely after parent cancellation")
	}
	close(blocked)
}
