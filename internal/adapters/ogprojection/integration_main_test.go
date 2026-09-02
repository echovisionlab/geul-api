//go:build integration

package ogprojection_test

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "cleanup OG projection integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
