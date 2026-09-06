package release

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestValidateReleaseCascadeAuthorizationBatchSize(t *testing.T) {
	require.NoError(t, validateReleaseCascadeAuthorizationBatchSize(maxReleaseCascadeAuthorizationTracks-1))
	require.NoError(t, validateReleaseCascadeAuthorizationBatchSize(maxReleaseCascadeAuthorizationTracks))
	err := validateReleaseCascadeAuthorizationBatchSize(maxReleaseCascadeAuthorizationTracks + 1)
	require.Equal(
		t,
		connect.CodeFailedPrecondition,
		connect.CodeOf(err),
	)
	require.Contains(t, err.Error(), "at most 999")
}
