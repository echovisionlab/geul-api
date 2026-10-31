//go:build integration

package testutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunBackendIntegrationBoundedCleanupAddsDeadline(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("cleanup failed")
	err := runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), backendIntegrationCleanupTimeout)
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
}
