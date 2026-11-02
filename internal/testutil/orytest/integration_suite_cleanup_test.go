//go:build integration

package orytest

import (
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := pgtest.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		code = 1
	}
	os.Exit(code)
}
