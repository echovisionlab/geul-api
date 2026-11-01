//go:build integration

// Package runtimetest checks File and media flows through the running API,
// media and CDN processes. Tests that call the File use cases in process stay
// in internal/filemedia, outside the runtime band.
package runtimetest

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
		fmt.Fprintf(os.Stderr, "start File/Media runtime integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close File/Media runtime integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
