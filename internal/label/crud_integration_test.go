//go:build integration

package label

import (
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// newLabelIntegrationService returns the manage Connect adapter of a Label
// service whose administrator is adminID.
func newLabelIntegrationService(t *testing.T, db *gorm.DB, adminID string) *ConnectHandler {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	syncArtistIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	return NewConnectHandler(newTestService(t, Deps{DB: db, SpiceDB: stack.SpiceDBClient}))
}
