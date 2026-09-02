//go:build integration

package main

import "github.com/echovisionlab/geul-api/internal/testutil"

func registerSuiteProcessCleanup(name string, cleanup func() error) {
	testutil.RegisterIntegrationProcessCleanup(name, cleanup)
}

func takeSuiteSignalCleanupOwnership() {
	testutil.TakeIntegrationSignalCleanupOwnership()
}
