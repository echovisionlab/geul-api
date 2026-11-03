//go:build integration

package criteria_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	criteriapostgres "github.com/echovisionlab/geul-api/internal/member/criteria/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := orytest.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Criteria integration suite: %v\n", err)
		os.Exit(1)
	}
	orytest.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	orytest.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Criteria integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

type noRecipients struct{}

func (noRecipients) Count(context.Context, membercriteria.Criteria) (int64, error) { return 0, nil }

// newIntegrationService is the production Criteria service over db and
// spiceDB, with writer for its audit records.
func newIntegrationService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, writer domainaudit.Appender) *membercriteria.Service {
	t.Helper()
	service, err := membercriteria.NewService(membercriteria.Deps{
		Units: criteriapostgres.NewUnits(db, campaignpostgres.CriteriaUsage{}), SpiceDB: spiceDB,
		Fence: identitystate.PostgresFence{}, Audit: writer, RecipientCounter: noRecipients{},
	})
	require.NoError(t, err)
	return service
}
