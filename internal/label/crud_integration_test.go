//go:build integration

package label_test

import (
	"testing"

	label "github.com/echovisionlab/geul-api/internal/label"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// newLabelIntegrationService returns the manage Connect adapter of a Label
// service whose administrator is adminID.
func newLabelIntegrationService(t *testing.T, db *gorm.DB, adminID string) *label.ConnectHandler {
	t.Helper()
	stack := orytest.SetupOryStack(t)
	syncLabelIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	return label.NewConnectHandler(newTestService(t, integrationDeps{DB: db, SpiceDB: stack.SpiceDBClient}))
}
