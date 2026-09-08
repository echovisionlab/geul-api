package jobregistry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryOwnsSessionLifecycleAndCancellation(t *testing.T) {
	t.Parallel()
	registry := &Registry{}
	first, started := registry.Start(context.Background(), "event-1", "file-1")
	require.True(t, started)
	require.NotNil(t, first)

	duplicate, started := registry.Start(context.Background(), "event-1", "file-1")
	require.False(t, started)
	require.Nil(t, duplicate)
	require.False(t, registry.CancelEvent("missing"))
	require.False(t, registry.CancelGroup("missing"))

	second, started := registry.Start(context.Background(), "event-2", "file-1")
	require.True(t, started)
	require.True(t, registry.CancelEvent("event-1"))
	require.ErrorIs(t, first.Context.Err(), context.Canceled)
	require.True(t, registry.CancelGroup("file-1"))
	require.ErrorIs(t, second.Context.Err(), context.Canceled)

	first.Close()
	first.Close()
	second.Close()
	require.False(t, registry.CancelEvent("event-1"))
	require.False(t, registry.CancelGroup("file-1"))
}
