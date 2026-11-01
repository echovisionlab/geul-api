//go:build integration

package public_test

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

// newPublicIntegrationDB is a PostgreSQL database with the app schema and
// the Kratos stub the Post author seed needs: the public reader is SQL over
// Series, Post and asset rows only.
func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
}
