//go:build integration

package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	return stack.Postgres.DB
}
