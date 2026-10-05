package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const memberAdminTestID = "11111111-1111-4111-8111-111111111111"

func TestMemberAdminToolDescriptors(t *testing.T) {
	_, err := NewMemberAdminTools(nil)
	require.Error(t, err)
	var missing *recordingMemberAdminReader
	_, err = NewMemberAdminTools(missing)
	require.Error(t, err)
	tools, err := NewMemberAdminTools(&recordingMemberAdminReader{})
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, []string{ToolMemberAdminList, ToolMemberAdminGet}, tools.ToolNames())
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, true, tool.Annotations["readOnlyHint"])
		require.Equal(t, false, tool.Annotations["destructiveHint"])
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			require.NoError(t, json.Unmarshal(schema, &object))
			require.Equal(t, "object", object["type"])
		}
	}
}

func TestMemberAdminListBuildsExactQueryAndPagination(t *testing.T) {
	for _, test := range []struct {
		arguments string
		limit     int32
		offset    int32
		filters   []*commonv1.FilterSpec
	}{
		{arguments: `{}`, limit: 20},
		{arguments: `{"query":"  user@example.test  ","status":"pending_deletion","limit":100,"offset":2147483647}`, limit: 100, offset: 2147483647,
			filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "user@example.test"}, {Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "pending_deletion"}}},
		{arguments: `{"query":"  ","status":"deleted","limit":1,"offset":0}`, limit: 1,
			filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "deleted"}}},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			reader := &recordingMemberAdminReader{list: &managev1.ListMembersAdminResponse{Pagination: &commonv1.PaginationResponse{Total: 7, Limit: test.limit, Offset: test.offset, HasMore: true}}}
			tools, err := NewMemberAdminTools(reader)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberAdminList, toolArguments(t, test.arguments))
			require.NoError(t, err)
			require.Equal(t, t.Context(), reader.ctx)
			require.Equal(t, test.limit, reader.listRequest.Pagination.Limit)
			require.Equal(t, test.offset, reader.listRequest.Pagination.Offset)
			require.Equal(t, test.filters, reader.listRequest.Filters)
			require.Empty(t, reader.listRequest.Sorts)
			require.Equal(t, []any{}, result.StructuredContent["members"])
			require.Equal(t, float64(7), result.StructuredContent["total"])
			require.Equal(t, true, result.StructuredContent["has_more"])
		})
	}
}

func TestMemberAdminGetProjectsBoundedDetailAndNativeStatuses(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(memberAdminOutputJSONSchema), &schema))
	accountSchema := schema["properties"].(map[string]any)["account"].(map[string]any)["properties"].(map[string]any)
	statuses := accountSchema["status"].(map[string]any)["enum"].([]any)
	for _, test := range []struct {
		status managev1.AccountStatus
		want   string
	}{
		{managev1.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED, "unspecified"},
		{managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE, "active"},
		{managev1.AccountStatus_ACCOUNT_STATUS_BANNED, "banned"},
		{managev1.AccountStatus_ACCOUNT_STATUS_PENDING_DELETION, "pending_deletion"},
		{managev1.AccountStatus_ACCOUNT_STATUS_DELETED, "deleted"},
	} {
		t.Run(test.want, func(t *testing.T) {
			bio, reason := "Member biography", "Account moderation reason"
			when := timestamppb.New(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC))
			reader := &recordingMemberAdminReader{member: &managev1.AdminMember{
				Member: &managev1.MemberProfile{Summary: &commonv1.MemberSummary{Id: memberAdminTestID, Nickname: "Member", AvatarAsset: &commonv1.AssetRef{AssetId: memberAdminTestID, Url: "https://private.example.test/avatar", Sha256: []byte("private-native-bytes")}}, Bio: &bio, CreatedAt: when},
				Account: &managev1.AccountSummary{Role: policyv1.AuthorizationRole_ADMIN, Status: test.status, CanonicalEmail: &managev1.CanonicalEmailSummary{Email: "member@example.test", Verified: true}, Banned: true,
					BanDetails: &managev1.AccountBanDetails{MetadataBanned: true, IdentityState: "inactive", InactiveState: true, Reason: &reason, ExpiresAt: when}},
				TagIds: []string{memberAdminTestID}, Onboarded: true, NewsletterSubscription: &managev1.NewsletterSubscriptionState{Subscribed: true, SubscribedAt: when},
				AccountDetails: &managev1.AccountAdminDetails{Providers: []*managev1.AccountProvider{{Provider: "github", Identifier: "provider-private-subject"}}, EmailCandidates: []*managev1.AccountEmailCandidate{{Email: "alternate-private@example.test"}}},
			}}
			tools, err := NewMemberAdminTools(reader)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberAdminGet, toolArguments(t, `{"member_id":"`+memberAdminTestID+`"}`))
			require.NoError(t, err)
			require.Equal(t, memberAdminTestID, reader.getRequest.MemberId)
			require.Equal(t, t.Context(), reader.ctx)
			member := result.StructuredContent["member"].(map[string]any)
			profile := member["profile"].(map[string]any)
			require.Equal(t, memberAdminTestID, profile["id"])
			require.Equal(t, memberAdminTestID, profile["avatar_asset_id"])
			require.Equal(t, bio, profile["bio"])
			require.Equal(t, "2026-10-05T01:02:03Z", profile["created_at"])
			account := member["account"].(map[string]any)
			require.Contains(t, statuses, account["status"])
			require.Equal(t, test.want, account["status"])
			require.Equal(t, "admin", account["role"])
			require.Equal(t, "member@example.test", account["canonical_email"].(map[string]any)["email"])
			require.Equal(t, true, account["canonical_email"].(map[string]any)["verified"])
			require.Equal(t, reason, account["ban_details"].(map[string]any)["reason"])
			require.Equal(t, true, member["onboarded"])
			require.Equal(t, true, member["newsletter_subscription"].(map[string]any)["subscribed"])
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			for _, excluded := range []string{"account_details", "provider-private-subject", "alternate-private@example.test", "credentials", "sessions", "tokens", "https://private.example.test/avatar", "sha256", "private-native-bytes"} {
				require.NotContains(t, string(encoded), excluded)
			}
		})
	}
}

func TestMemberAdminProfileOmitsAbsentAvatar(t *testing.T) {
	projected := projectMemberAdmin(&managev1.AdminMember{Member: &managev1.MemberProfile{Summary: &commonv1.MemberSummary{Id: memberAdminTestID}}})
	encoded, err := json.Marshal(projected)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "avatar_asset_id")
}

func TestMemberAdminRejectsInvalidArgumentsBeforeReading(t *testing.T) {
	for _, test := range []struct{ tool, arguments string }{
		{ToolMemberAdminList, `{"limit":0}`}, {ToolMemberAdminList, `{"limit":101}`}, {ToolMemberAdminList, `{"limit":-1}`}, {ToolMemberAdminList, `{"limit":null}`},
		{ToolMemberAdminList, `{"offset":-1}`}, {ToolMemberAdminList, `{"offset":2147483648}`}, {ToolMemberAdminList, `{"offset":null}`}, {ToolMemberAdminList, `{"offset":1.5}`},
		{ToolMemberAdminList, `{"status":""}`}, {ToolMemberAdminList, `{"status":"pending"}`}, {ToolMemberAdminList, `{"status":null}`}, {ToolMemberAdminList, `{"query":null}`}, {ToolMemberAdminList, `{"role":"admin"}`},
		{ToolMemberAdminGet, `{}`}, {ToolMemberAdminGet, `{"member_id":null}`}, {ToolMemberAdminGet, `{"member_id":"MEMBER"}`}, {ToolMemberAdminGet, `{"member_id":"11111111111141118111111111111111"}`},
	} {
		t.Run(test.tool+test.arguments, func(t *testing.T) {
			reader := &recordingMemberAdminReader{}
			tools, err := NewMemberAdminTools(reader)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Zero(t, reader.calls)
		})
	}
}

func TestMemberAdminPreservesOwningDenialWithoutPersonalData(t *testing.T) {
	for _, name := range []string{ToolMemberAdminList, ToolMemberAdminGet} {
		t.Run(name, func(t *testing.T) {
			reader := &recordingMemberAdminReader{err: connect.NewError(connect.CodePermissionDenied, errors.New("Administrator permission required"))}
			tools, err := NewMemberAdminTools(reader)
			require.NoError(t, err)
			arguments := toolArguments(t, `{}`)
			if name == ToolMemberAdminGet {
				arguments = toolArguments(t, `{"member_id":"`+memberAdminTestID+`"}`)
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Equal(t, "Administrator permission required", execution.Message)
			require.Empty(t, result.Content)
			require.Nil(t, result.StructuredContent)
			require.Equal(t, 1, reader.calls)
		})
	}
}

func TestMemberAdminUsesNativeAuthenticationBoundary(t *testing.T) {
	// The real Member service rejects before touching any unset dependencies.
	tools, err := NewMemberAdminTools(&memberdomain.MemberService{})
	require.NoError(t, err)
	for _, name := range []string{ToolMemberAdminList, ToolMemberAdminGet} {
		arguments := toolArguments(t, `{}`)
		if name == ToolMemberAdminGet {
			arguments = toolArguments(t, `{"member_id":"`+memberAdminTestID+`"}`)
		}
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution)
		require.Empty(t, result.Content)
		require.Nil(t, result.StructuredContent)
	}
}

type recordingMemberAdminReader struct {
	list        *managev1.ListMembersAdminResponse
	member      *managev1.AdminMember
	err         error
	listRequest *managev1.ListMembersAdminRequest
	getRequest  *managev1.GetMemberRequest
	ctx         context.Context
	calls       int
}

func (reader *recordingMemberAdminReader) ListMembersAdmin(ctx context.Context, request *connect.Request[managev1.ListMembersAdminRequest]) (*connect.Response[managev1.ListMembersAdminResponse], error) {
	reader.ctx, reader.listRequest = ctx, request.Msg
	reader.calls++
	return connect.NewResponse(reader.list), reader.err
}

func (reader *recordingMemberAdminReader) GetMember(ctx context.Context, request *connect.Request[managev1.GetMemberRequest]) (*connect.Response[managev1.AdminMember], error) {
	reader.ctx, reader.getRequest = ctx, request.Msg
	reader.calls++
	return connect.NewResponse(reader.member), reader.err
}

func TestMemberAdminKeepsInternalFailurePrivateAndUnknownToolDistinct(t *testing.T) {
	failure := connect.NewError(connect.CodeInternal, errors.New("private audit storage detail"))
	reader := &recordingMemberAdminReader{err: failure}
	tools, err := NewMemberAdminTools(reader)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberAdminGet, toolArguments(t, `{"member_id":"`+memberAdminTestID+`"}`))
	require.ErrorIs(t, err, failure)
	var execution *mcpserver.ToolExecutionError
	require.False(t, errors.As(err, &execution))
	require.Empty(t, result.Content)
	require.Nil(t, result.StructuredContent)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, "member_admin_missing", toolArguments(t, `{}`))
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
	require.Equal(t, 1, reader.calls)
}
