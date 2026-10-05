//go:build integration

package member

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// MCPAdminReadFixtureForTest exposes only the controls needed by the external
// MCP test. Keeping its seeds here reuses Member's private integration helpers
// without a Member -> MCP -> Filemedia -> Member import cycle.
type MCPAdminReadFixtureForTest struct {
	Service       *MemberService
	AdminContext  context.Context
	AuthorContext context.Context
	UserContext   context.Context
	AdminMemberID string
	TargetID      string
	TargetEmail   string
	writer        *personalDataAccessWriterStub
}

func NewMCPAdminReadFixtureForTest(t *testing.T) MCPAdminReadFixtureForTest {
	t.Helper()
	db := newServiceIntegrationDB(t)
	spiceDB := testutil.IntegrationSpiceDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	admin := seedMemberAdminListPair(t, db, spiceDB, "MCP admin", "mcp-admin@example.test", policyv1.Role.Admin(), false, false, now)
	author := seedMemberAdminListPair(t, db, spiceDB, "MCP author", "mcp-author@example.test", policyv1.Role.Author(), false, false, now)
	user := seedMemberAdminListPair(t, db, spiceDB, "MCP user", "mcp-user@example.test", policyv1.Role.User(), false, false, now)
	targetEmail := "mcp-private-target@example.test"
	target := seedMemberAdminListPair(t, db, spiceDB, "MCP target", targetEmail, policyv1.Role.User(), true, false, now)
	writer := &personalDataAccessWriterStub{}
	service := NewAuditedMemberService(db, "", spiceDB,
		&fakeIdentityManager{identity: &auth.Identity{ID: target.identityID, ExternalID: target.memberID, Traits: map[string]interface{}{"email": targetEmail}}},
		personalDataFileDeleter{}, "", personalDataEmailPublisher{}, writer,
		WithAccountSummaryReader(integrationAccountSummaryReader{}), WithAccountEmailProjection(integrationAccountEmailProjection{}),
	)
	return MCPAdminReadFixtureForTest{Service: service, AdminContext: personalAccessAdminContext(t, admin), AuthorContext: personalAccessAdminContext(t, author),
		UserContext: personalAccessAdminContext(t, user), AdminMemberID: admin.memberID, TargetID: target.memberID, TargetEmail: targetEmail, writer: writer}
}

func (fixture MCPAdminReadFixtureForTest) AuditRecords() []sharedtelemetry.SecurityAccessRecord {
	return fixture.writer.records
}

func (fixture MCPAdminReadFixtureForTest) FailAudit(err error) {
	fixture.writer.err = err
}
