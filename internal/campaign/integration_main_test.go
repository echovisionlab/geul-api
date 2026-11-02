//go:build integration

package campaign_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Campaign integration suite: %v\n", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := pgtest.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "cleanup Campaign integration runtime: %v\n", err)
		code = 1
	}
	orytest.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Campaign integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
