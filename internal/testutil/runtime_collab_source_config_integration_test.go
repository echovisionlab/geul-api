//go:build integration

package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeCollabSourceEnvironmentUsesCurrentWorkspaceContracts(t *testing.T) {
	t.Parallel()

	require.Equal(t, map[string]string{
		"TSX_TSCONFIG_PATH": "tsconfig.local-dependencies.json",
	}, runtimeCollabSourceEnv())
}
