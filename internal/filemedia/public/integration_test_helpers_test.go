//go:build integration

package public

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var publicIntegrationSpiceDB *auth.SpiceDBClient

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start public File/Media integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close public File/Media integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	publicIntegrationSpiceDB = stack.SpiceDBClient
	return stack.DB
}

func publicIntegrationSpiceDBClient(t *testing.T) *auth.SpiceDBClient {
	t.Helper()
	if publicIntegrationSpiceDB == nil {
		stack := testutil.SetupOryStack(t)
		publicIntegrationSpiceDB = stack.SpiceDBClient
	}
	return publicIntegrationSpiceDB
}
