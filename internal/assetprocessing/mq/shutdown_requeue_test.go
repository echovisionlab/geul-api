package mq

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShutdownRequeueContextKeepsDeliveryValuesAfterCancellation(t *testing.T) {
	type contextKey struct{}
	parent, shutdown := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "delivery"))
	shutdown()

	retryCtx, cancel := shutdownRequeueContext(parent)
	defer cancel()
	require.NoError(t, retryCtx.Err())
	require.Equal(t, "delivery", retryCtx.Value(contextKey{}))
	deadline, ok := retryCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), shutdownRequeueTimeout)
}
