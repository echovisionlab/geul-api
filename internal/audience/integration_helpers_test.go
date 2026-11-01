//go:build integration

package audience_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	audiencedomain "github.com/echovisionlab/geul-api/internal/audience"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Audience integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Audience integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

type noRecipients struct{}

func (noRecipients) Count(context.Context, audiencedomain.Segment) (int64, error) { return 0, nil }

// newIntegrationService is the production Audience service over db and
// spiceDB, with writer for its audit records.
func newIntegrationService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, writer domainaudit.Appender) *audiencedomain.Service {
	t.Helper()
	service, err := audiencedomain.NewService(audiencedomain.Deps{
		Units: audiencepostgres.NewUnits(db, campaign.SegmentUsage{}), SpiceDB: spiceDB,
		Fence: identitystate.PostgresFence{}, Audit: writer, RecipientCounter: noRecipients{},
	})
	require.NoError(t, err)
	return service
}
