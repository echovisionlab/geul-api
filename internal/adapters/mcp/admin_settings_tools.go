package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	ToolSiteSettingsGet         = "site_settings_get"
	ToolSiteSettingsUpdate      = "site_settings_update"
	ToolMailAdapterList         = "mail_adapter_list"
	ToolMailAdapterSenderUpdate = "mail_adapter_sender_update"
)

var adminSettingsTools = []mcpserver.Tool{
	oauthTool(ToolSiteSettingsGet, "Get site settings", "Read administrator site settings, source File IDs (including the full, symbol and email logos, light and dark), social links, branding, contact details, navigation, OG configuration, and read-only runtime site_origin. Requires the admin UI's Site Settings view permission. File IDs are source IDs, not rendered asset IDs.", adminSettingsEmptyInputJSONSchema, siteSettingsGetOutputJSONSchema, true, false),
	oauthTool(ToolSiteSettingsUpdate, "Update site settings", "Atomically update only supplied settings through the admin UI's Site Settings service, validation and audit. Read site_settings_get first. Omitted keys stay unchanged; null clears or resets values according to the existing admin service. social_links and og_image_config replace whole objects; dotted OG section keys replace only that section. To set a logo, upload the SVG/PNG with file_upload kind=image (email logos must be PNG) and assign its File UUID to the matching logo_* key. Loader files, default_map_theme_id and runtime site_origin are read-only here; use map_theme_set_default for the default theme.", siteSettingsUpdateInputJSONSchema, siteSettingsUpdateOutputJSONSchema, false, false),
	oauthTool(ToolMailAdapterList, "List mail adapters", "List administrator mail adapters in priority order with safe sender and provider metadata. Requires the admin UI's Mail Adapter list permission. Credentials, access keys, passwords, usernames and tokens are excluded. Continue with next_offset while has_more.", mailAdapterListInputJSONSchema, mailAdapterListOutputJSONSchema, true, false),
	oauthTool(ToolMailAdapterSenderUpdate, "Update mail adapter sender", "Partially update from_name/from_email and optionally is_active/priority for a mail adapter ID from mail_adapter_list. Supply at least one of from_name, from_email, is_active, or priority. Omitted fields and all stored credentials remain unchanged; an empty from_name clears the display name. Sender changes require SES or SMTP. Uses the admin UI's fresh Mail Adapter update permission, audit and provider verification, which may send a verification email to the resulting sender address. No credentials are accepted or returned.", mailAdapterSenderUpdateInputJSONSchema, mailAdapterSenderUpdateOutputJSONSchema, false, false),
}

// These ports retain the same application services and request context as RPC.
type SiteSettingsAdminApplication interface {
	GetSettings(context.Context) (sitesettings.AdminSettings, error)
	GetSetting(context.Context, sitesettings.GetSettingQuery) (sitesettings.Setting, error)
	SetManySettings(context.Context, sitesettings.SetManySettingsCommand) (sitesettings.SettingsUpdate, error)
}

type MailAdapterAdminApplication interface {
	ListMailAdapters(context.Context, emaildelivery.ListMailAdaptersQuery) (emaildelivery.MailAdapterPage, error)
	UpdateMailAdapterSender(context.Context, emaildelivery.MailAdapterSenderUpdate) (*managev1.MailAdapter, error)
}

type AdminSettingsTools struct {
	settings SiteSettingsAdminApplication
	mail     MailAdapterAdminApplication
}

func NewAdminSettingsTools(settings SiteSettingsAdminApplication, mail MailAdapterAdminApplication) (*AdminSettingsTools, error) {
	if interfaceValueIsNil(settings) || interfaceValueIsNil(mail) {
		return nil, errors.New("MCP administrator Site Settings and Mail Adapter applications are required")
	}
	return &AdminSettingsTools{settings: settings, mail: mail}, nil
}

func (*AdminSettingsTools) ToolNames() []string { return toolDefinitionNames(adminSettingsTools) }
func (*AdminSettingsTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(adminSettingsTools), nil
}

func (tools *AdminSettingsTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	result, err := tools.callTool(ctx, name, arguments)
	if err == nil || errors.Is(err, mcpserver.ErrUnknownTool) {
		return result, err
	}
	return expectedToolError(err)
}

func (tools *AdminSettingsTools) callTool(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolSiteSettingsGet:
		if err := decodeArguments(arguments, &struct{}{}); err != nil {
			return executionError(err)
		}
		all, err := tools.settings.GetSettings(ctx)
		if err != nil {
			return mcpserver.ToolResult{}, err
		}
		settings := projectSiteSettings(all)
		// Rendered Asset IDs are not source File IDs. Fetch the original bindings
		// via the existing authorized single-setting API used by the admin UI.
		for _, key := range siteSettingsFileKeys {
			binding, err := tools.settings.GetSetting(ctx, sitesettings.GetSettingQuery{Key: key})
			if err != nil {
				return mcpserver.ToolResult{}, err
			}
			settings[key] = binding.Value
		}
		return adminSettingsResult(map[string]any{"settings": settings})
	case ToolSiteSettingsUpdate:
		var input struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if len(input.Settings) == 0 {
			return executionError(errors.New("settings must contain at least one supported setting"))
		}
		keys := make([]string, 0, len(input.Settings))
		for key := range input.Settings {
			if !siteSettingsMutableKey(key) {
				return executionError(fmt.Errorf("unsupported site setting: %s", key))
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		settings := make([]sitesettings.Setting, 0, len(keys))
		for _, key := range keys {
			value := &structpb.Value{}
			if err := value.UnmarshalJSON(input.Settings[key]); err != nil {
				return executionError(fmt.Errorf("invalid value for setting %s", key))
			}
			settings = append(settings, sitesettings.Setting{Key: key, Value: value.AsInterface()})
		}
		update, err := tools.settings.SetManySettings(ctx, sitesettings.SetManySettingsCommand{Settings: settings})
		if err != nil {
			return mcpserver.ToolResult{}, err
		}
		return adminSettingsResult(map[string]any{"success": true, "og_generation_run_id": update.OGGenerationRunID})
	case ToolMailAdapterList:
		if err := rejectNullArguments(arguments, "limit", "offset"); err != nil {
			return executionError(err)
		}
		var input struct {
			Limit  *int32 `json:"limit"`
			Offset int32  `json:"offset"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		limit := int32(20)
		if input.Limit != nil {
			limit = *input.Limit
		}
		if limit < 1 || limit > 100 || input.Offset < 0 {
			return executionError(errors.New("limit must be between 1 and 100; offset must be between 0 and 2147483647"))
		}
		page, err := tools.mail.ListMailAdapters(ctx, emaildelivery.ListMailAdaptersQuery{Limit: limit, Offset: input.Offset})
		if err != nil {
			return mcpserver.ToolResult{}, err
		}
		adapters := make([]mailAdapterAdminOutput, 0, len(page.Adapters))
		for _, adapter := range page.Adapters {
			adapters = append(adapters, projectMailAdapterAdmin(adapter))
		}
		pagination := page.Pagination
		next := int64(input.Offset) + int64(len(adapters))
		hasMore := len(adapters) > 0 && next < int64(pagination.GetTotal()) && next <= 1<<31-1
		output := map[string]any{"adapters": adapters, "total": pagination.GetTotal(), "limit": limit, "offset": input.Offset, "has_more": hasMore}
		if hasMore {
			output["next_offset"] = next
		}
		return adminSettingsResult(output)
	case ToolMailAdapterSenderUpdate:
		if err := rejectNullArguments(arguments, "id", "from_name", "from_email", "is_active", "priority"); err != nil {
			return executionError(err)
		}
		var input struct {
			ID        string  `json:"id"`
			FromName  *string `json:"from_name"`
			FromEmail *string `json:"from_email"`
			IsActive  *bool   `json:"is_active"`
			Priority  *int32  `json:"priority"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("id", input.ID); err != nil {
			return executionError(err)
		}
		if input.FromName == nil && input.FromEmail == nil && input.IsActive == nil && input.Priority == nil {
			return executionError(errors.New("supply at least one of from_name, from_email, is_active, or priority"))
		}
		adapter, err := tools.mail.UpdateMailAdapterSender(ctx, emaildelivery.MailAdapterSenderUpdate{
			ID: input.ID, FromName: input.FromName, FromEmail: input.FromEmail, IsActive: input.IsActive, Priority: input.Priority,
		})
		if err != nil {
			return mcpserver.ToolResult{}, err
		}
		return adminSettingsResult(map[string]any{"adapter": projectMailAdapterAdmin(adapter)})
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

// siteSettingsFileKeys are the source File settings; reads resolve each through
// GetSetting and every one of them is writable.
var siteSettingsFileKeys = []string{
	"logo_full_light_file_id", "logo_full_dark_file_id",
	"logo_symbol_light_file_id", "logo_symbol_dark_file_id",
	"logo_email_light_file_id", "logo_email_dark_file_id",
	"favicon_file_id", "site_og_background_file_id", "privacy_og_background_file_id", "terms_og_background_file_id",
}

func siteSettingsMutableKey(key string) bool {
	if slices.Contains(siteSettingsFileKeys, key) {
		return true
	}
	switch key {
	case "site_title", "company_name", "company_address", "tax_id", "legal_email", "contact_email", "social_links",
		"primary_color", "default_comments_enabled", "homepage_page_id", "menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id",
		"meta_description", "google_analytics_id", "og_image_config", "og_image_config.home", "og_image_config.content":
		return true
	default:
		return false
	}
}

func projectSiteSettings(settings sitesettings.AdminSettings) map[string]any {
	loaders := make([]string, 0, len(settings.LoaderAssets))
	for _, loader := range settings.LoaderAssets {
		loaders = append(loaders, loader.FileID)
	}
	return map[string]any{
		"site_title": settings.SiteTitle, "company_name": settings.CompanyName, "company_address": settings.CompanyAddress, "tax_id": settings.TaxID,
		"legal_email": settings.LegalEmail, "contact_email": settings.ContactEmail,
		"social_links": storedObject(settings.SocialLinks), "primary_color": settings.PrimaryColor, "default_comments_enabled": settings.DefaultCommentsEnabled,
		"homepage_page_id": settings.HomepagePageID, "menu_header_id": settings.MenuHeaderID, "menu_secondary_id": settings.MenuSecondaryID,
		"menu_footer_id": settings.MenuFooterID, "menu_avatar_dropdown_id": settings.MenuAvatarDropdownID,
		"meta_description": settings.MetaDescription, "google_analytics_id": settings.GoogleAnalyticsID,
		"loader_file_ids": loaders, "default_map_theme_id": settings.DefaultMapThemeID,
		"og_image_config": storedObject(settings.OGImageConfig), "site_origin": settings.SiteOrigin,
	}
}

// storedObject is a decoded JSON setting as the tool reports it: an object,
// empty when none is stored.
func storedObject(fields structured.Fields) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	return fields
}

// Explicit projection: even a service accidentally returning secrets cannot
// expand the MCP surface through protobuf serialization.
type mailAdapterAdminOutput struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	IsActive  bool   `json:"is_active"`
	Priority  int32  `json:"priority"`
	FromName  string `json:"from_name"`
	FromEmail string `json:"from_email"`
	Region    string `json:"region,omitempty"`
	Host      string `json:"host,omitempty"`
	Port      int32  `json:"port,omitempty"`
	Secure    *bool  `json:"secure,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func projectMailAdapterAdmin(adapter *managev1.MailAdapter) mailAdapterAdminOutput {
	output := mailAdapterAdminOutput{ID: adapter.GetId(), Name: adapter.GetName(), Type: strings.ToLower(strings.TrimPrefix(adapter.GetType().String(), "MAIL_ADAPTER_TYPE_")),
		IsActive: adapter.GetIsActive(), Priority: adapter.GetPriority(), CreatedAt: adapter.GetCreatedAt(), UpdatedAt: adapter.GetUpdatedAt()}
	if config := adapter.GetSesConfig(); config != nil {
		output.FromName, output.FromEmail, output.Region = config.GetFromName(), config.GetFromEmail(), config.GetRegion()
	}
	if config := adapter.GetSmtpConfig(); config != nil {
		output.FromName, output.FromEmail, output.Host, output.Port = config.GetFromName(), config.GetFromEmail(), config.GetHost(), config.GetPort()
		secure := config.GetSecure()
		output.Secure = &secure
	}
	return output
}

func adminSettingsResult(value any) (mcpserver.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP administrator settings result: %w", err)
	}
	return structuredResult(encoded, false)
}
