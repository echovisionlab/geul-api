package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

const adminSettingsTestID = "11111111-1111-4111-8111-111111111111"

func TestAdminSettingsToolDescriptors(t *testing.T) {
	owner := &recordingAdminSettingsApplication{}
	var missing *recordingAdminSettingsApplication
	for _, pair := range [][2]*recordingAdminSettingsApplication{{nil, owner}, {owner, nil}, {missing, owner}, {owner, missing}} {
		_, err := NewAdminSettingsTools(pair[0], pair[1])
		require.Error(t, err)
	}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 4)
	require.Equal(t, []string{ToolSiteSettingsGet, ToolSiteSettingsUpdate, ToolMailAdapterList, ToolMailAdapterSenderUpdate}, tools.ToolNames())
	set, err := NewToolSet(tools)
	require.NoError(t, err)
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, tool.Name == ToolSiteSettingsGet || tool.Name == ToolMailAdapterList, tool.Annotations["readOnlyHint"])
		require.Equal(t, false, tool.Annotations["destructiveHint"])
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			require.NoError(t, json.Unmarshal(schema, &object))
			require.Equal(t, "object", object["type"])
			require.Equal(t, false, object["additionalProperties"])
		}
	}
	_, err = set.CallTool(t.Context(), mcpserver.Principal{}, "admin_settings_missing", toolArguments(t, `{}`))
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
	require.Zero(t, owner.calls)
}

func TestSiteSettingsGetReturnsAdminValuesAndSourceFileIDs(t *testing.T) {
	social, err := structpb.NewStruct(map[string]any{"website": "https://example.test"})
	require.NoError(t, err)
	og, err := structpb.NewStruct(map[string]any{"home": map[string]any{"title": "Home"}})
	require.NoError(t, err)
	owner := &recordingAdminSettingsApplication{settings: &managev1.AllSettings{
		Public: &managev1.PublicSettings{
			SiteTitle: "Site", CompanyName: "Company", CompanyAddress: "Address", TaxId: "Tax",
			LegalEmail: "legal@example.test", ContactEmail: "contact@example.test",
			MetaDescription: "Description", PrimaryColor: "#abcdef", DefaultCommentsEnabled: true, SocialLinks: social,
			HomepagePageId: adminSettingsPointer(adminSettingsTestID), MenuHeaderId: adminSettingsPointer(adminSettingsTestID),
			GoogleAnalyticsId: adminSettingsPointer("G-ABC"), DefaultMapThemeId: adminSettingsTestID,
			LogoFullLightAsset: &commonv1.AssetRef{AssetId: "rendered-asset-must-not-be-file-id", Url: "https://cdn.example.test/logo"},
			LoaderAssets:       []*managev1.SiteLoaderAsset{{FileId: adminSettingsTestID}},
		},
		OgImageConfig: og, Runtime: &managev1.RuntimeSettings{SiteOrigin: "https://example.test"},
	}}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolSiteSettingsGet, toolArguments(t, `{}`))
	require.NoError(t, err)
	require.Equal(t, t.Context(), owner.ctx)
	require.Equal(t, siteSettingsFileKeys, owner.getKeys)
	require.Equal(t, 1+len(siteSettingsFileKeys), owner.calls)
	settings := result.StructuredContent["settings"].(map[string]any)
	for key, value := range map[string]any{
		"site_title": "Site", "company_name": "Company", "company_address": "Address", "tax_id": "Tax", "meta_description": "Description",
		"legal_email": "legal@example.test", "contact_email": "contact@example.test",
		"primary_color": "#abcdef", "default_comments_enabled": true, "homepage_page_id": adminSettingsTestID,
		"menu_header_id": adminSettingsTestID, "google_analytics_id": "G-ABC", "default_map_theme_id": adminSettingsTestID,
		"site_origin": "https://example.test", "logo_full_light_file_id": adminSettingsTestID,
	} {
		require.Equal(t, value, settings[key], key)
	}
	for _, key := range []string{"support_email", "privacy_email"} {
		require.NotContains(t, settings, key)
	}
	require.Nil(t, settings["favicon_file_id"])
	require.Nil(t, settings["menu_footer_id"])
	require.Equal(t, []any{adminSettingsTestID}, settings["loader_file_ids"])
	require.Equal(t, social.AsMap(), settings["social_links"])
	require.Equal(t, og.AsMap(), settings["og_image_config"])
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "rendered-asset-must-not-be-file-id")
}

func TestSiteSettingsUpdateForwardsOnlySuppliedValuesAtomically(t *testing.T) {
	owner := &recordingAdminSettingsApplication{}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolSiteSettingsUpdate, toolArguments(t,
		`{"settings":{"site_title":"New title","favicon_file_id":null,"default_comments_enabled":false,"social_links":{},"og_image_config.home":{"title":"New home"}}}`))
	require.NoError(t, err)
	require.Equal(t, 1, owner.calls)
	require.Equal(t, t.Context(), owner.ctx)
	settingsByKey := map[string]any{}
	for _, setting := range owner.setCommand.Settings {
		settingsByKey[setting.Key] = setting.Value
	}
	require.Equal(t, map[string]any{"site_title": "New title", "favicon_file_id": nil, "default_comments_enabled": false, "social_links": map[string]any{}, "og_image_config.home": map[string]any{"title": "New home"}}, settingsByKey)
	require.Equal(t, true, result.StructuredContent["success"])
	require.Nil(t, result.StructuredContent["og_generation_run_id"])
}

func TestSiteSettingsSchemaAndRuntimeAllowlistAgree(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(siteSettingsUpdateInputJSONSchema), &schema))
	properties := schema["properties"].(map[string]any)["settings"].(map[string]any)["properties"].(map[string]any)
	require.Len(t, properties, 29)
	for key := range properties {
		require.True(t, siteSettingsMutableKey(key), key)
	}
	for _, key := range []string{"default_map_theme_id", "site_origin", "loader_file_ids", "smtp_password", "support_email", "privacy_email"} {
		require.False(t, siteSettingsMutableKey(key))
	}
}

func TestMailAdapterListPaginationAndCredentialExclusion(t *testing.T) {
	for _, arguments := range []string{`{}`, `{"limit":1,"offset":2}`} {
		t.Run(arguments, func(t *testing.T) {
			owner := &recordingAdminSettingsApplication{adapters: []*managev1.MailAdapter{adminSettingsSESAdapter(), adminSettingsSMTPAdapter()}, total: 7}
			tools, err := NewAdminSettingsTools(owner, owner)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterList, toolArguments(t, arguments))
			require.NoError(t, err)
			require.Equal(t, t.Context(), owner.ctx)
			limit, offset := int32(20), int32(0)
			if arguments != `{}` {
				limit, offset = 1, 2
			}
			require.Equal(t, emaildelivery.ListMailAdaptersQuery{Limit: limit, Offset: offset}, owner.listQuery)
			require.Equal(t, true, result.StructuredContent["has_more"])
			require.Equal(t, float64(offset+2), result.StructuredContent["next_offset"])
			adapters := result.StructuredContent["adapters"].([]any)
			ses, smtp := adapters[0].(map[string]any), adapters[1].(map[string]any)
			require.Equal(t, "ses", ses["type"])
			require.Equal(t, "ap-northeast-2", ses["region"])
			require.Equal(t, "Sender", ses["from_name"])
			require.Equal(t, "sender@example.test", ses["from_email"])
			require.Equal(t, "smtp", smtp["type"])
			require.Equal(t, "smtp.example.test", smtp["host"])
			require.Equal(t, false, smtp["secure"])
			assertAdminSettingsNoSecrets(t, result)
		})
	}
	owner := &recordingAdminSettingsApplication{}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterList, toolArguments(t, `{}`))
	require.NoError(t, err)
	require.Equal(t, []any{}, result.StructuredContent["adapters"])
	require.Equal(t, false, result.StructuredContent["has_more"])
	require.NotContains(t, result.StructuredContent, "next_offset")
}

func TestMailAdapterSenderUpdatePreservesPresenceAndExcludesCredentials(t *testing.T) {
	for _, arguments := range []string{
		`{"id":"` + adminSettingsTestID + `","from_name":""}`,
		`{"id":"` + adminSettingsTestID + `","from_email":"new@example.test","is_active":false,"priority":0}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			owner := &recordingAdminSettingsApplication{adapter: adminSettingsSESAdapter()}
			tools, err := NewAdminSettingsTools(owner, owner)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterSenderUpdate, toolArguments(t, arguments))
			require.NoError(t, err)
			require.Equal(t, t.Context(), owner.ctx)
			require.Equal(t, 1, owner.calls)
			require.Equal(t, adminSettingsTestID, owner.sender.ID)
			if owner.sender.FromName != nil {
				require.Empty(t, *owner.sender.FromName)
				require.Nil(t, owner.sender.FromEmail)
				require.Nil(t, owner.sender.IsActive)
				require.Nil(t, owner.sender.Priority)
			} else {
				require.Equal(t, "new@example.test", *owner.sender.FromEmail)
				require.False(t, *owner.sender.IsActive)
				require.Zero(t, *owner.sender.Priority)
			}
			assertAdminSettingsNoSecrets(t, result)
		})
	}
}

func TestMailAdapterSenderUpdateAllowsOnlyActivationOrPriority(t *testing.T) {
	for _, arguments := range []string{
		`{"id":"` + adminSettingsTestID + `","is_active":false}`,
		`{"id":"` + adminSettingsTestID + `","priority":-2147483648}`,
	} {
		t.Run(arguments, func(t *testing.T) {
			owner := &recordingAdminSettingsApplication{adapter: adminSettingsSMTPAdapter()}
			tools, err := NewAdminSettingsTools(owner, owner)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterSenderUpdate, toolArguments(t, arguments))
			require.NoError(t, err)
			require.Equal(t, 1, owner.calls)
			require.Nil(t, owner.sender.FromName)
			require.Nil(t, owner.sender.FromEmail)
			if owner.sender.IsActive != nil {
				require.False(t, *owner.sender.IsActive)
				require.Nil(t, owner.sender.Priority)
			} else {
				require.Equal(t, int32(-2147483648), *owner.sender.Priority)
			}
			assertAdminSettingsNoSecrets(t, result)
		})
	}
}

func TestMailAdapterListStopsAtLastPageAndInt32OffsetBoundary(t *testing.T) {
	for _, test := range []struct {
		arguments string
		total     int32
	}{
		{`{"limit":1,"offset":2}`, 3},
		{`{"limit":1,"offset":2147483647}`, 2147483647},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			owner := &recordingAdminSettingsApplication{adapters: []*managev1.MailAdapter{adminSettingsSESAdapter()}, total: test.total}
			tools, err := NewAdminSettingsTools(owner, owner)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterList, toolArguments(t, test.arguments))
			require.NoError(t, err)
			require.Equal(t, false, result.StructuredContent["has_more"])
			require.NotContains(t, result.StructuredContent, "next_offset")
		})
	}
}

func TestAdminSettingsRejectsMalformedArgumentsBeforeCallingServices(t *testing.T) {
	for _, test := range []struct{ name, arguments string }{
		{ToolSiteSettingsGet, `{"extra":true}`},
		{ToolSiteSettingsUpdate, `{}`}, {ToolSiteSettingsUpdate, `{"settings":null}`}, {ToolSiteSettingsUpdate, `{"settings":{}}`},
		{ToolSiteSettingsUpdate, `{"settings":{"site_origin":"https://example.test"}}`},
		{ToolSiteSettingsUpdate, `{"settings":{"default_map_theme_id":"x"}}`}, {ToolSiteSettingsUpdate, `{"settings":{"loader_file_ids":[]}}`},
		{ToolSiteSettingsUpdate, `{"settings":{"unknown":"x"}}`}, {ToolSiteSettingsUpdate, `{"settings":{"site_title":"x"},"extra":true}`},
		{ToolMailAdapterList, `{"limit":null}`}, {ToolMailAdapterList, `{"offset":null}`}, {ToolMailAdapterList, `{"limit":0}`},
		{ToolMailAdapterList, `{"limit":101}`}, {ToolMailAdapterList, `{"offset":-1}`}, {ToolMailAdapterList, `{"offset":2147483648}`},
		{ToolMailAdapterList, `{"limit":1.5}`}, {ToolMailAdapterList, `{"secrets":true}`},
		{ToolMailAdapterSenderUpdate, `{}`}, {ToolMailAdapterSenderUpdate, `{"id":"bad","from_name":"Sender"}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `"}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","from_name":null}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","from_email":null}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","priority":2147483648}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","is_active":null}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","from_name":"Sender","secret_access_key":"never"}`},
		{ToolMailAdapterSenderUpdate, `{"id":"` + adminSettingsTestID + `","from_name":"Sender","ses_config":{}}`},
	} {
		t.Run(test.name+test.arguments, func(t *testing.T) {
			owner := &recordingAdminSettingsApplication{}
			tools, err := NewAdminSettingsTools(owner, owner)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Zero(t, owner.calls)
		})
	}
}

func TestAdminSettingsPreservesServiceDenialsAndValidation(t *testing.T) {
	for _, name := range []string{ToolSiteSettingsGet, ToolSiteSettingsUpdate, ToolMailAdapterList, ToolMailAdapterSenderUpdate} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeFailedPrecondition} {
			t.Run(name+code.String(), func(t *testing.T) {
				owner := &recordingAdminSettingsApplication{err: connect.NewError(code, errors.New("Owning service rejected request"))}
				tools, err := NewAdminSettingsTools(owner, owner)
				require.NoError(t, err)
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, adminSettingsArguments(t, name))
				var execution *mcpserver.ToolExecutionError
				require.ErrorAs(t, err, &execution)
				require.Equal(t, "Owning service rejected request", execution.Message)
				require.Empty(t, result.Content)
				require.Nil(t, result.StructuredContent)
				require.Equal(t, 1, owner.calls)
			})
		}
	}
}

func TestAdminSettingsUsesNativeAuthenticationBoundary(t *testing.T) {
	tools, err := NewAdminSettingsTools(&sitesettings.Service{}, &emaildelivery.Service{})
	require.NoError(t, err)
	for _, name := range tools.ToolNames() {
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, adminSettingsArguments(t, name))
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution)
		require.Empty(t, result.Content)
		require.Nil(t, result.StructuredContent)
	}
}

// The MCP handler logs a tool failure once; the adapter returns the original
// cause with it so that record keeps the cause behind a mapped message.
func TestAdminSettingsReturnsOriginalFailureCauseAndKeepsInternalFailurePrivate(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	failure := connect.NewError(connect.CodeInternal, errors.New("private storage detail password=private-password sender@example.test"))
	owner := &recordingAdminSettingsApplication{err: failure}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterList, toolArguments(t, `{}`))
	require.ErrorIs(t, err, failure)
	var execution *mcpserver.ToolExecutionError
	require.False(t, errors.As(err, &execution))
	require.Empty(t, result.Content)
	require.Nil(t, result.StructuredContent)
	cause := failurecause.Message(err)
	require.Contains(t, cause, "private storage detail")
	require.NotContains(t, cause, "private-password")
	require.NotContains(t, cause, "sender@example.test")
	owner.err = connect.NewError(connect.CodeUnavailable, errors.New("original upstream failure"))
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMailAdapterList, toolArguments(t, `{}`))
	require.ErrorAs(t, err, &execution)
	require.Equal(t, "The service is temporarily unavailable", execution.Message)
	require.Contains(t, failurecause.Message(execution.Cause), "original upstream failure")
	require.Empty(t, logs.String())
}

func TestSiteSettingsGetBindingFailureReturnsNoPartialSettings(t *testing.T) {
	owner := &recordingAdminSettingsApplication{bindingErr: connect.NewError(connect.CodePermissionDenied, errors.New("Administrator permission changed"))}
	tools, err := NewAdminSettingsTools(owner, owner)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolSiteSettingsGet, toolArguments(t, `{}`))
	var execution *mcpserver.ToolExecutionError
	require.ErrorAs(t, err, &execution)
	require.Nil(t, result.StructuredContent)
	require.Empty(t, result.Content)
}

func adminSettingsArguments(t *testing.T, name string) mcpserver.ToolArguments {
	t.Helper()
	switch name {
	case ToolSiteSettingsUpdate:
		return toolArguments(t, `{"settings":{"site_title":"Site"}}`)
	case ToolMailAdapterSenderUpdate:
		return toolArguments(t, `{"id":"`+adminSettingsTestID+`","from_name":"Sender"}`)
	default:
		return toolArguments(t, `{}`)
	}
}

func adminSettingsPointer[T any](value T) *T { return &value }

func adminSettingsSESAdapter() *managev1.MailAdapter {
	return &managev1.MailAdapter{Id: adminSettingsTestID, Name: "SES", Type: managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES, IsActive: true, Priority: 3,
		Config: &managev1.MailAdapter_SesConfig{SesConfig: &managev1.SESConfig{Region: "ap-northeast-2", FromName: adminSettingsPointer("Sender"), FromEmail: "sender@example.test", AccessKeyId: "private-access-key", SecretAccessKey: "private-secret-key"}}}
}

func adminSettingsSMTPAdapter() *managev1.MailAdapter {
	return &managev1.MailAdapter{Id: adminSettingsTestID, Name: "SMTP", Type: managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP,
		Config: &managev1.MailAdapter_SmtpConfig{SmtpConfig: &managev1.SMTPConfig{Host: "smtp.example.test", Port: 587, FromEmail: "sender@example.test", User: "private-username", Password: "private-password"}}}
}

func assertAdminSettingsNoSecrets(t *testing.T, result mcpserver.ToolResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	for _, secret := range []string{"private-access-key", "private-secret-key", "private-username", "private-password", "access_key", "secret", "password", "token", "\"user\""} {
		require.NotContains(t, string(encoded), secret)
	}
}

type recordingAdminSettingsApplication struct {
	settings   *managev1.AllSettings
	adapters   []*managev1.MailAdapter
	adapter    *managev1.MailAdapter
	total      int32
	err        error
	bindingErr error
	ctx        context.Context
	calls      int
	getKeys    []string
	setCommand sitesettings.SetManySettingsCommand
	listQuery  emaildelivery.ListMailAdaptersQuery
	sender     emaildelivery.MailAdapterSenderUpdate
}

func (owner *recordingAdminSettingsApplication) GetSettings(ctx context.Context) (*managev1.AllSettings, error) {
	owner.ctx = ctx
	owner.calls++
	return owner.settings, owner.err
}

func (owner *recordingAdminSettingsApplication) GetSetting(ctx context.Context, query sitesettings.GetSettingQuery) (*managev1.SiteSetting, error) {
	owner.ctx = ctx
	owner.calls++
	owner.getKeys = append(owner.getKeys, query.Key)
	value := structpb.NewNullValue()
	if query.Key == "logo_full_light_file_id" {
		value = structpb.NewStringValue(adminSettingsTestID)
	}
	return &managev1.SiteSetting{Key: query.Key, Value: value}, owner.bindingErr
}

func (owner *recordingAdminSettingsApplication) SetManySettings(ctx context.Context, command sitesettings.SetManySettingsCommand) (sitesettings.SettingsUpdate, error) {
	owner.ctx, owner.setCommand = ctx, command
	owner.calls++
	return sitesettings.SettingsUpdate{}, owner.err
}

func (owner *recordingAdminSettingsApplication) ListMailAdapters(ctx context.Context, query emaildelivery.ListMailAdaptersQuery) (emaildelivery.MailAdapterPage, error) {
	owner.ctx, owner.listQuery = ctx, query
	owner.calls++
	return emaildelivery.MailAdapterPage{Adapters: owner.adapters, Pagination: &commonv1.PaginationResponse{Total: owner.total, Limit: query.Limit, Offset: query.Offset}}, owner.err
}

func (owner *recordingAdminSettingsApplication) UpdateMailAdapterSender(ctx context.Context, input emaildelivery.MailAdapterSenderUpdate) (*managev1.MailAdapter, error) {
	owner.ctx, owner.sender = ctx, input
	owner.calls++
	return owner.adapter, owner.err
}

func TestSiteSettingsUpdateRejectsLegacyContactKeys(t *testing.T) {
	for _, key := range []string{"support_email", "privacy_email"} {
		owner := &recordingAdminSettingsApplication{}
		tools, err := NewAdminSettingsTools(owner, owner)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolSiteSettingsUpdate,
			toolArguments(t, `{"settings":{"`+key+`":"contact@example.test"}}`))
		var executionErr *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &executionErr)
		require.Contains(t, executionErr.Message, "unsupported site setting: "+key)
		require.Zero(t, owner.calls)
	}
}
