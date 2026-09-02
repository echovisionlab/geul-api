//go:build integration

package og_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start OG integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "cleanup OG integration suite: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "close OG integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
