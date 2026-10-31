//go:build integration

package public_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start public Series integration suite:", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "close public Series integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB { return testutil.NewIntegrationDB(t) }
