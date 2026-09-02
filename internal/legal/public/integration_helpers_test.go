//go:build integration

package public_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"gorm.io/gorm"
)

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		ApplyAppSchemaSQL: true,
	}).DB
}
