//go:build integration

package main

import "github.com/echovisionlab/geul-api/internal/testutil/pgtest"

func registerSuiteProcessCleanup(name string, cleanup func() error) {
	pgtest.RegisterIntegrationProcessCleanup(name, cleanup)
}

func takeSuiteSignalCleanupOwnership() {
	pgtest.TakeIntegrationSignalCleanupOwnership()
}
