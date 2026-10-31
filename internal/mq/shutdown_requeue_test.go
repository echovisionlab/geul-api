package mq

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShutdownRequeueContextKeepsDeliveryValuesAfterCancellation(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "delivery"))
	cancel()

	requeueCtx, requeueCancel := shutdownRequeueContext(ctx)
	defer requeueCancel()
	require.NoError(t, requeueCtx.Err())
	require.Equal(t, "delivery", requeueCtx.Value(contextKey{}))
	deadline, ok := requeueCtx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), shutdownRequeueTimeout)
}
