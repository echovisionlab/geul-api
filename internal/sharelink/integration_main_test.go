//go:build integration

package sharelink_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Share Link Ory integration suite:", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "cleanup Share Link Ory integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}
