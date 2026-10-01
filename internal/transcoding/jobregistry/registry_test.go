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
	require.True(t, IsExplicitCancellation(first.Context))
	require.True(t, registry.CancelGroup("file-1"))
	require.ErrorIs(t, second.Context.Err(), context.Canceled)
	require.True(t, IsExplicitCancellation(second.Context))

	first.Close()
	first.Close()
	second.Close()
	require.False(t, registry.CancelEvent("event-1"))
	require.False(t, registry.CancelGroup("file-1"))
}

func TestRegistryKeepsParentShutdownDistinctFromExplicitCancellation(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	registry := &Registry{}
	session, started := registry.Start(parent, "shutdown-event", "file-1")
	require.True(t, started)

	stop()
	require.ErrorIs(t, session.Context.Err(), context.Canceled)
	require.False(t, IsExplicitCancellation(session.Context))
	session.Close()
	require.False(t, registry.CancelGroup("file-1"), "closed sessions must not leave cancellation markers")
}
