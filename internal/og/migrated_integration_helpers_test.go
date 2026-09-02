//go:build integration

package og_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"gorm.io/gorm"
)

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.NewIntegrationDB(t)
}

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.NewConcurrentPostIntegrationDB(t)
}
