//go:build integration

package member_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcpadapter "github.com/echovisionlab/geul-api/internal/adapters/mcp"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/member"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestMemberMCPAdminReadAuthorizationIntegration(t *testing.T) {
	fixture := member.NewMCPAdminReadFixtureForTest(t)
	tools, err := mcpadapter.NewMemberAdminTools(fixture.Service)
	require.NoError(t, err)
	listArguments := memberMCPReadArguments(t, map[string]any{"query": fixture.TargetEmail})
	getArguments := memberMCPReadArguments(t, map[string]any{"member_id": fixture.TargetID})
	list, err := tools.CallTool(fixture.AdminContext, mcpserver.Principal{}, mcpadapter.ToolMemberAdminList, listArguments)
	require.NoError(t, err)
	members := list.StructuredContent["members"].([]any)
	require.Len(t, members, 1)
	memberMCPAssertTarget(t, members[0].(map[string]any), fixture.TargetID, fixture.TargetEmail)
	detail, err := tools.CallTool(fixture.AdminContext, mcpserver.Principal{}, mcpadapter.ToolMemberAdminGet, getArguments)
	require.NoError(t, err)
	memberMCPAssertTarget(t, detail.StructuredContent["member"].(map[string]any), fixture.TargetID, fixture.TargetEmail)
	records := fixture.AuditRecords()
	require.Len(t, records, 2)
	wantScopes := map[string]int{"member_collection:1:member_administration": 1, "member:" + fixture.TargetID + ":member_administration": 1}
	for _, record := range records {
		require.Equal(t, sharedtelemetry.SecurityPersonalDataAccessed, record.Action)
		require.Equal(t, fixture.AdminMemberID, record.MemberID)
		require.Equal(t, sharedtelemetry.PersonalDataAccessRead, record.AccessKind)
		wantScopes[record.SubjectType+":"+record.SubjectID+":"+record.DataCategory]--
	}
	for scope, remaining := range wantScopes {
		require.Zero(t, remaining, scope)
	}
	for _, actor := range []struct {
		name string
		ctx  context.Context
	}{{"author", fixture.AuthorContext}, {"user", fixture.UserContext}} {
		t.Run(actor.name, func(t *testing.T) {
			for _, call := range []struct {
				name      string
				arguments mcpserver.ToolArguments
			}{{mcpadapter.ToolMemberAdminList, listArguments}, {mcpadapter.ToolMemberAdminGet, getArguments}} {
				result, err := tools.CallTool(actor.ctx, mcpserver.Principal{}, call.name, call.arguments)
				var execution *mcpserver.ToolExecutionError
				require.ErrorAs(t, err, &execution)
				require.Contains(t, execution.Message, "admin")
				require.NotContains(t, execution.Message, fixture.TargetEmail)
				require.NotContains(t, execution.Message, fixture.TargetID)
				require.Empty(t, result.Content)
				require.Nil(t, result.StructuredContent)
			}
		})
	}
	require.Len(t, fixture.AuditRecords(), 2, "denied calls must not reach personal-data access")
}

func TestMemberMCPAdminReadAuditFailureIntegration(t *testing.T) {
	fixture := member.NewMCPAdminReadFixtureForTest(t)
	fixture.FailAudit(errors.New("private audit database failure"))
	tools, err := mcpadapter.NewMemberAdminTools(fixture.Service)
	require.NoError(t, err)
	for _, call := range []struct {
		name      string
		arguments mcpserver.ToolArguments
	}{
		{mcpadapter.ToolMemberAdminList, memberMCPReadArguments(t, map[string]any{"query": fixture.TargetEmail})},
		{mcpadapter.ToolMemberAdminGet, memberMCPReadArguments(t, map[string]any{"member_id": fixture.TargetID})},
	} {
		result, err := tools.CallTool(fixture.AdminContext, mcpserver.Principal{}, call.name, call.arguments)
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution)
		require.Equal(t, "The service is temporarily unavailable", execution.Message)
		require.Empty(t, result.Content)
		require.Nil(t, result.StructuredContent)
	}
	require.Len(t, fixture.AuditRecords(), 2, "both owning reads must try their access audit and fail closed")
}

func memberMCPReadArguments(t *testing.T, input map[string]any) mcpserver.ToolArguments {
	t.Helper()
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	var arguments mcpserver.ToolArguments
	require.NoError(t, json.Unmarshal(encoded, &arguments))
	return arguments
}

func memberMCPAssertTarget(t *testing.T, projected map[string]any, id, email string) {
	t.Helper()
	profile := projected["profile"].(map[string]any)
	require.Equal(t, id, profile["id"])
	require.Equal(t, "MCP target", profile["nickname"])
	account := projected["account"].(map[string]any)
	require.Equal(t, "user", account["role"])
	require.Equal(t, "active", account["status"])
	require.Equal(t, email, account["canonical_email"].(map[string]any)["email"])
	require.Equal(t, true, projected["newsletter_subscription"].(map[string]any)["subscribed"])
	require.NotContains(t, projected, "account_details")
}
