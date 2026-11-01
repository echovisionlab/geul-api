//go:build integration

package public_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"gorm.io/gorm"
)

// newPublicIntegrationDB is a PostgreSQL database with the app schema and
// the Kratos stub the Post author seed needs: the public reader is SQL over
// Series, Post and asset rows only.
func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
}
