package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
)

const (
	ToolOAuthAppList                 = "oauth_app_list"
	ToolOAuthAppGet                  = "oauth_app_get"
	ToolOAuthAppCreate               = "oauth_app_create"
	ToolOAuthAppUpdate               = "oauth_app_update"
	ToolOAuthAppDelete               = "oauth_app_delete"
	ToolOAuthAppSecretRotate         = "oauth_app_secret_rotate"
	ToolOAuthConnectedToolList       = "oauth_connected_tool_list"
	ToolOAuthConnectedToolDisconnect = "oauth_connected_tool_disconnect"
	ToolMemberCriteriaList           = "member_criteria_list"
)

var oauthAppTools = toolRegistry[OAuthAppTools]{
	defineTool(oauthTool(ToolOAuthAppList, "List sign-in apps", "List the apps an Admin registered to sign people in with their site account, with who may sign in and each app's revision. Administrator only.", oauthAppEmptyInputJSONSchema, oauthAppListOutputJSONSchema, true, false), (*OAuthAppTools).list),
	defineTool(oauthTool(ToolOAuthAppGet, "Get sign-in app", "Read one sign-in app and its current revision. Read it before oauth_app_update, oauth_app_delete or oauth_app_secret_rotate.", oauthAppClientIDInputJSONSchema, oauthAppOutputJSONSchema, true, false), (*OAuthAppTools).get),
	defineTool(oauthTool(ToolOAuthAppCreate, "Create sign-in app", "Register an app that signs people in with their site account. Apps get no refresh token, so every sign-in checks access again. A confidential app's client_secret is returned only in this result; give it to the app's operator and do not store it elsewhere.", oauthAppCreateInputJSONSchema, oauthAppSecretOutputJSONSchema, false, false), (*OAuthAppTools).create),
	defineTool(oauthTool(ToolOAuthAppUpdate, "Update sign-in app", "Replace every setting of a sign-in app with the revision from oauth_app_get; copy the settings you keep from that read. Switching a public app to a client secret method returns a new client_secret once.", oauthAppUpdateInputJSONSchema, oauthAppSecretOutputJSONSchema, false, false), (*OAuthAppTools).update),
	defineTool(oauthTool(ToolOAuthAppDelete, "Delete sign-in app", "Delete a sign-in app with the revision from oauth_app_get. It can no longer sign anyone in and its tokens stop working.", oauthAppRevisionInputJSONSchema, oauthAppDeletedOutputJSONSchema, false, true), (*OAuthAppTools).delete),
	defineTool(oauthTool(ToolOAuthAppSecretRotate, "Issue new sign-in app secret", "Replace the client secret of a confidential sign-in app with the revision from oauth_app_get. The previous secret stops working at once; the new one is returned only in this result.", oauthAppRevisionInputJSONSchema, oauthAppSecretOutputJSONSchema, false, true), (*OAuthAppTools).rotate),
	defineTool(oauthTool(ToolOAuthConnectedToolList, "List connected AI tools", "List the AI tools that registered themselves to use the site's editing tools. Administrator only.", oauthAppEmptyInputJSONSchema, oauthConnectedToolListOutputJSONSchema, true, false), (*OAuthAppTools).listTools),
	defineTool(oauthTool(ToolOAuthConnectedToolDisconnect, "Disconnect AI tool", "Disconnect an AI tool from oauth_connected_tool_list. It loses access at once and connects again only after a Member approves it again.", oauthAppClientIDInputJSONSchema, oauthConnectedToolDisconnectOutputJSONSchema, false, true), (*OAuthAppTools).disconnect),
	defineTool(oauthTool(ToolMemberCriteriaList, "List Member criteria", "List or search the active Member criteria that can admit Members to a sign-in app or a restricted download. Use the IDs in member_criteria_ids. Continue with the same query and next_offset.", memberCriteriaListInputJSONSchema, memberCriteriaListOutputJSONSchema, true, false), (*OAuthAppTools).listCriteria),
}

// OAuthAppAdminApplication is the OAuth app use cases the tools call.
type OAuthAppAdminApplication interface {
	ListSignInApps(context.Context) ([]oauthapp.App, error)
	GetSignInApp(context.Context, string) (oauthapp.App, error)
	CreateSignInApp(context.Context, oauthapp.CreateCommand) (oauthapp.SavedApp, error)
	UpdateSignInApp(context.Context, oauthapp.UpdateCommand) (oauthapp.SavedApp, error)
	DeleteSignInApp(context.Context, oauthapp.AppCommand) error
	RotateSignInAppSecret(context.Context, oauthapp.AppCommand) (oauthapp.SavedApp, error)
	ListConnectedTools(context.Context) ([]oauthapp.ConnectedTool, error)
	DisconnectConnectedTool(context.Context, string) error
}

// MemberCriteriaReader lists the Member criteria that can admit Members.
type MemberCriteriaReader interface {
	ListAuthenticatedAccessCriteriaSet(context.Context, membercriteria.ListAuthenticatedAccessCriteriaSetQuery) (membercriteria.AccessCriteriaPage, error)
}

type OAuthAppTools struct {
	apps     OAuthAppAdminApplication
	criteria MemberCriteriaReader
}

func NewOAuthAppTools(apps OAuthAppAdminApplication, criteria MemberCriteriaReader) (*OAuthAppTools, error) {
	if interfaceValueIsNil(apps) || interfaceValueIsNil(criteria) {
		return nil, errors.New("MCP OAuth app application and Member criteria reader are required")
	}
	return &OAuthAppTools{apps: apps, criteria: criteria}, nil
}

func (*OAuthAppTools) ToolNames() []string { return oauthAppTools.names() }

func (*OAuthAppTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return oauthAppTools.list(), nil
}

func (tools *OAuthAppTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return oauthAppTools.call(tools, ctx, name, arguments)
}

type oauthAppSettingsArguments struct {
	Name                   string   `json:"name"`
	Access                 string   `json:"access"`
	MemberCriteriaIDs      []string `json:"member_criteria_ids"`
	RedirectURIs           []string `json:"redirect_uris"`
	PostLogoutRedirectURIs []string `json:"post_logout_redirect_uris"`
	Scopes                 []string `json:"scopes"`
	AuthMethod             string   `json:"auth_method"`
	AccessTokenLifespan    string   `json:"access_token_lifespan"`
	IDTokenLifespan        string   `json:"id_token_lifespan"`
}

type oauthAppClientIDArguments struct {
	ClientID string `json:"client_id"`
}

type oauthAppRevisionArguments struct {
	ClientID         string `json:"client_id"`
	ExpectedRevision string `json:"expected_revision"`
}

type oauthAppCreateArguments struct {
	ClientID string                    `json:"client_id"`
	Settings oauthAppSettingsArguments `json:"settings"`
}

type oauthAppUpdateArguments struct {
	ClientID         string                    `json:"client_id"`
	ExpectedRevision string                    `json:"expected_revision"`
	Settings         oauthAppSettingsArguments `json:"settings"`
}

type memberCriteriaListArguments struct {
	Query  string `json:"query"`
	Limit  *int32 `json:"limit"`
	Offset int32  `json:"offset"`
}

func (tools *OAuthAppTools) list(ctx context.Context, _ string, call toolCall[struct{}]) (mcpserver.ToolResult, error) {
	if err := call.Decode(&struct{}{}); err != nil {
		return executionError(err)
	}
	apps, err := tools.apps.ListSignInApps(ctx)
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, len(apps))
	for i, app := range apps {
		items[i] = oauthAppOutput(app)
	}
	return contentResult(map[string]any{"apps": items})
}

func (tools *OAuthAppTools) get(ctx context.Context, _ string, call toolCall[oauthAppClientIDArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppClientIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	app, err := tools.apps.GetSignInApp(ctx, input.ClientID)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"app": oauthAppOutput(app)})
}

func (tools *OAuthAppTools) create(ctx context.Context, _ string, call toolCall[oauthAppCreateArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	settings, err := oauthAppSettings(input.Settings)
	if err != nil {
		return executionError(err)
	}
	saved, err := tools.apps.CreateSignInApp(ctx, oauthapp.CreateCommand{ClientID: input.ClientID, Settings: settings})
	if err != nil {
		return expectedToolError(err)
	}
	return savedAppResult(saved)
}

func (tools *OAuthAppTools) update(ctx context.Context, _ string, call toolCall[oauthAppUpdateArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppUpdateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	settings, err := oauthAppSettings(input.Settings)
	if err != nil {
		return executionError(err)
	}
	saved, err := tools.apps.UpdateSignInApp(ctx, oauthapp.UpdateCommand{
		ClientID: input.ClientID, ExpectedRevision: input.ExpectedRevision, Settings: settings,
	})
	if err != nil {
		return expectedToolError(err)
	}
	return savedAppResult(saved)
}

func (tools *OAuthAppTools) delete(ctx context.Context, _ string, call toolCall[oauthAppRevisionArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppRevisionArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := tools.apps.DeleteSignInApp(ctx, oauthapp.AppCommand{ClientID: input.ClientID, ExpectedRevision: input.ExpectedRevision}); err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"client_id": strings.TrimSpace(input.ClientID), "deleted": true})
}

func (tools *OAuthAppTools) rotate(ctx context.Context, _ string, call toolCall[oauthAppRevisionArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppRevisionArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	saved, err := tools.apps.RotateSignInAppSecret(ctx, oauthapp.AppCommand{ClientID: input.ClientID, ExpectedRevision: input.ExpectedRevision})
	if err != nil {
		return expectedToolError(err)
	}
	return savedAppResult(saved)
}

func (tools *OAuthAppTools) listTools(ctx context.Context, _ string, call toolCall[struct{}]) (mcpserver.ToolResult, error) {
	if err := call.Decode(&struct{}{}); err != nil {
		return executionError(err)
	}
	connected, err := tools.apps.ListConnectedTools(ctx)
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, len(connected))
	for i, tool := range connected {
		item := map[string]any{
			"client_id": tool.ClientID, "name": tool.Name, "stale": tool.Stale, "redirect_uris": nonNilStrings(tool.RedirectURIs),
			"resource": tool.Resource, "auth_method": tool.AuthMethod,
		}
		if tool.Publisher != nil {
			item["publisher"] = *tool.Publisher
		}
		if tool.CreatedAt != nil {
			item["created_at"] = tool.CreatedAt.UTC().Format(time.RFC3339)
		}
		items[i] = item
	}
	return contentResult(map[string]any{"tools": items})
}

func (tools *OAuthAppTools) disconnect(ctx context.Context, _ string, call toolCall[oauthAppClientIDArguments]) (mcpserver.ToolResult, error) {
	var input oauthAppClientIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := tools.apps.DisconnectConnectedTool(ctx, input.ClientID); err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"client_id": strings.TrimSpace(input.ClientID), "disconnected": true})
}

func (tools *OAuthAppTools) listCriteria(ctx context.Context, _ string, call toolCall[memberCriteriaListArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "query", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input memberCriteriaListArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	limit := int32(50)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 100 || input.Offset < 0 {
		return executionError(errors.New("limit must be between 1 and 100 and offset must not be negative"))
	}
	page, err := tools.criteria.ListAuthenticatedAccessCriteriaSet(ctx, membercriteria.ListAuthenticatedAccessCriteriaSetQuery{
		Limit: limit, Offset: input.Offset, Filters: discoverySearchFilters(input.Query),
	})
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, len(page.CriteriaSet))
	for i, criteria := range page.CriteriaSet {
		item := map[string]any{"id": criteria.ID, "name": criteria.Name, "kind": strings.TrimPrefix(string(criteria.Type), "MEMBER_CRITERIA_KIND_")}
		if criteria.Description != nil {
			item["description"] = *criteria.Description
		}
		items[i] = item
	}
	hasMore := int64(page.Offset)+int64(len(page.CriteriaSet)) < page.Total
	output := map[string]any{"items": items, "total": page.Total, "limit": page.Limit, "offset": page.Offset, "has_more": hasMore}
	if hasMore {
		output["next_offset"] = int64(page.Offset) + int64(len(page.CriteriaSet))
	}
	return contentResult(output)
}

var oauthAppLifespans = map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "1h": time.Hour}

// oauthAppSettings reads the tool's lowercase values; the use case validates
// the rest.
func oauthAppSettings(input oauthAppSettingsArguments) (oauthapp.Settings, error) {
	access := map[string]oauthapp.Access{
		"none": oauthapp.AccessNone, "admins": oauthapp.AccessAdmins, "members": oauthapp.AccessMembers,
		"member_criteria": oauthapp.AccessMemberCriteria,
	}[input.Access]
	if access == "" {
		return oauthapp.Settings{}, errors.New("settings.access must be none, admins, members or member_criteria")
	}
	accessLifespan, ok := oauthAppLifespans[input.AccessTokenLifespan]
	if !ok {
		return oauthapp.Settings{}, errors.New("settings.access_token_lifespan must be 5m, 15m or 1h")
	}
	idLifespan, ok := oauthAppLifespans[input.IDTokenLifespan]
	if !ok {
		return oauthapp.Settings{}, errors.New("settings.id_token_lifespan must be 5m, 15m or 1h")
	}
	scopes := make([]oauthapp.Scope, len(input.Scopes))
	for i, scope := range input.Scopes {
		scopes[i] = oauthapp.Scope(scope)
	}
	return oauthapp.Settings{
		Name: input.Name, Access: access, MemberCriteriaIDs: input.MemberCriteriaIDs, RedirectURIs: input.RedirectURIs,
		PostLogoutRedirectURIs: input.PostLogoutRedirectURIs, Scopes: scopes, AuthMethod: oauthapp.AuthMethod(input.AuthMethod),
		AccessTokenLifespan: accessLifespan, IDTokenLifespan: idLifespan,
	}, nil
}

func savedAppResult(saved oauthapp.SavedApp) (mcpserver.ToolResult, error) {
	output := map[string]any{"app": oauthAppOutput(saved.App)}
	if saved.Secret != nil {
		output["client_secret"] = *saved.Secret
	}
	return contentResult(output)
}

func oauthAppOutput(app oauthapp.App) map[string]any {
	settings := app.Settings
	access := strings.ToLower(strings.TrimPrefix(string(settings.Access), "SIGN_IN_ACCESS_"))
	criteria := make([]map[string]any, len(app.MemberCriteria))
	for i, summary := range app.MemberCriteria {
		criteria[i] = map[string]any{
			"id": summary.ID, "name": summary.Name, "kind": strings.TrimPrefix(summary.Kind, "MEMBER_CRITERIA_KIND_"), "eligible": summary.Eligible,
		}
	}
	scopes := make([]string, len(settings.Scopes))
	for i, scope := range settings.Scopes {
		scopes[i] = string(scope)
	}
	output := map[string]any{
		"client_id": app.ClientID, "revision": app.Revision, "name": settings.Name, "access": access,
		"member_criteria": criteria, "redirect_uris": nonNilStrings(settings.RedirectURIs),
		"post_logout_redirect_uris": nonNilStrings(settings.PostLogoutRedirectURIs), "scopes": scopes,
		"auth_method": string(settings.AuthMethod), "access_token_lifespan": lifespanLabel(settings.AccessTokenLifespan),
		"id_token_lifespan": lifespanLabel(settings.IDTokenLifespan), "created_at": app.CreatedAt.UTC().Format(time.RFC3339),
	}
	if app.UpdatedAt != nil {
		output["updated_at"] = app.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return output
}

func lifespanLabel(lifespan time.Duration) string {
	for label, value := range oauthAppLifespans {
		if value == lifespan {
			return label
		}
	}
	return ""
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
