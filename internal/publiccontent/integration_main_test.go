//go:build integration

package publiccontent

import (
	"flag"
	"os"
	"testing"

	// Register the shared integration-lease flags before the test binary parses
	// the harness arguments. This package uses SQLite and does not start the
	// leased PostgreSQL or Ory resources.
	_ "github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}
