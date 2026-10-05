package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolMapThemeList           = "map_theme_list"
	ToolMapThemeResolve        = "map_theme_resolve"
	ToolMapThemeGet            = "map_theme_get"
	ToolMapThemeCreate         = "map_theme_create"
	ToolMapThemeDelete         = "map_theme_delete"
	ToolMapThemeCopy           = "map_theme_copy"
	ToolMapThemeSetDefault     = "map_theme_set_default"
	ToolMapThemeSettingsUpdate = "map_theme_settings_update"
)

// MapThemeManagement consumes management operations only; authorization and
// request-actor audit remain in MapThemeService, outside collaboration saves.
type MapThemeManagement interface {
	ListMapThemes(context.Context, *connect.Request[managev1.ListMapThemesRequest]) (*connect.Response[managev1.ListMapThemesResponse], error)
	ResolveMapTheme(context.Context, *connect.Request[managev1.ResolveMapThemeRequest]) (*connect.Response[managev1.ResolvedMapTheme], error)
	GetMapTheme(context.Context, *connect.Request[managev1.GetMapThemeRequest]) (*connect.Response[managev1.MapTheme], error)
	CreateMapTheme(context.Context, *connect.Request[managev1.CreateMapThemeRequest]) (*connect.Response[managev1.MapTheme], error)
	DeleteMapTheme(context.Context, *connect.Request[managev1.DeleteMapThemeRequest]) (*connect.Response[managev1.DeleteResponse], error)
	CopyMapTheme(context.Context, *connect.Request[managev1.CopyMapThemeRequest]) (*connect.Response[managev1.MapTheme], error)
	SetDefaultMapTheme(context.Context, *connect.Request[managev1.SetDefaultMapThemeRequest]) (*connect.Response[managev1.SetDefaultMapThemeResponse], error)
	UpdateMapThemeSnapshot(context.Context, string, int64, *managev1.CreateMapThemeRequest) (*managev1.MapTheme, bool, error)
}

var mapThemeManagementTools = []mcpserver.Tool{
	oauthTool(ToolMapThemeList, "List map themes", "List current map themes and the site default through the existing management reader.", mapThemeListInputJSONSchema, mapThemeListOutputJSONSchema, true, false),
	oauthTool(ToolMapThemeResolve, "Resolve a map theme", "Resolve the requested theme or current site default, using light or dark scheme. A missing requested theme falls back to the default.", mapThemeResolveInputJSONSchema, mapThemeResolveOutputJSONSchema, true, false),
	oauthTool(ToolMapThemeGet, "Get map theme settings", "Read a map theme's revision and complete editable snapshot. Requires existing administrator view permission.", mapThemeIDInputJSONSchema, mapThemeOutputJSONSchema, true, false),
	oauthTool(ToolMapThemeCreate, "Create a map theme", "Create a theme from the complete snapshot shape. Requires existing administrator creation permission.", mapThemeSnapshotInputJSONSchema, mapThemeOutputJSONSchema, false, false),
	oauthTool(ToolMapThemeDelete, "Delete a map theme", "Delete a theme through its existing administrator policy. Select another site default before deleting the current default.", mapThemeIDInputJSONSchema, mapThemeDeleteOutputJSONSchema, false, true),
	oauthTool(ToolMapThemeCopy, "Copy a map theme", "Copy an existing theme under a new name through the administrator creation policy.", mapThemeCopyInputJSONSchema, mapThemeOutputJSONSchema, false, false),
	oauthTool(ToolMapThemeSetDefault, "Set the default map theme", "Select the site's default theme through its existing administrator management policy.", mapThemeIDInputJSONSchema, mapThemeDefaultOutputJSONSchema, false, false),
	oauthTool(ToolMapThemeSettingsUpdate, "Update map theme settings", "Replace the complete editable snapshot using the revision from map_theme_get. Requires current administrator edit permission; identical normalized values are a no-op. Reload after a revision conflict.", mapThemeUpdateInputJSONSchema, mapThemeUpdateOutputJSONSchema, false, false),
}

type MapThemeManagementTools struct{ service MapThemeManagement }

func NewMapThemeManagementTools(service MapThemeManagement) (*MapThemeManagementTools, error) {
	if interfaceValueIsNil(service) {
		return nil, errors.New("MCP Map Theme management is required")
	}
	return &MapThemeManagementTools{service: service}, nil
}
func (*MapThemeManagementTools) ToolNames() []string {
	return toolDefinitionNames(mapThemeManagementTools)
}
func (*MapThemeManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(mapThemeManagementTools), nil
}

func (tools *MapThemeManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolMapThemeList:
		if err := decodeArguments(arguments, &struct{}{}); err != nil {
			return executionError(err)
		}
		response, err := tools.service.ListMapThemes(ctx, connect.NewRequest(&managev1.ListMapThemesRequest{}))
		if err != nil {
			return expectedToolError(err)
		}
		themes := make([]mapThemeOutput, 0, len(response.Msg.Themes))
		for _, theme := range response.Msg.Themes {
			themes = append(themes, projectMapTheme(theme))
		}
		return mapThemeResult(struct {
			Themes    []mapThemeOutput `json:"themes"`
			DefaultID string           `json:"default_map_theme_id"`
		}{themes, response.Msg.DefaultMapThemeId})
	case ToolMapThemeResolve:
		if err := rejectNullArguments(arguments, "theme_id", "scheme"); err != nil {
			return executionError(err)
		}
		var input struct {
			ThemeID *string `json:"theme_id"`
			Scheme  string  `json:"scheme"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if input.ThemeID != nil {
			if err := validateUUID("theme_id", *input.ThemeID); err != nil {
				return executionError(err)
			}
		}
		if _, present := arguments["scheme"]; !present {
			input.Scheme = "light"
		}
		if input.Scheme != "light" && input.Scheme != "dark" {
			return executionError(errors.New("scheme must be light or dark"))
		}
		response, err := tools.service.ResolveMapTheme(ctx, connect.NewRequest(&managev1.ResolveMapThemeRequest{ThemeId: input.ThemeID, Scheme: input.Scheme}))
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			ThemeID  string           `json:"theme_id"`
			Scheme   string           `json:"scheme"`
			Settings mapThemeSettings `json:"settings"`
			Variant  mapThemeVariant  `json:"variant"`
		}{response.Msg.ThemeId, response.Msg.Scheme, projectMapThemeSettings(response.Msg.Settings), projectMapThemeVariant(response.Msg.Variant)})
	case ToolMapThemeCreate:
		var snapshot mapThemeSnapshot
		if err := decodeArguments(arguments, &snapshot); err != nil {
			return executionError(err)
		}
		raw, err := json.Marshal(arguments)
		if err != nil {
			return executionError(err)
		}
		if err := validateMapThemeSnapshotShape(raw, &snapshot); err != nil {
			return executionError(err)
		}
		response, err := tools.service.CreateMapTheme(ctx, connect.NewRequest(snapshot.native()))
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			Theme mapThemeOutput `json:"theme"`
		}{projectMapTheme(response.Msg)})
	case ToolMapThemeSettingsUpdate:
		if err := rejectNullArguments(arguments, "expected_revision", "snapshot"); err != nil {
			return executionError(err)
		}
		var input struct {
			ThemeID          string            `json:"theme_id"`
			ExpectedRevision int64             `json:"expected_revision"`
			Snapshot         *mapThemeSnapshot `json:"snapshot"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("theme_id", input.ThemeID); err != nil {
			return executionError(err)
		}
		if input.ExpectedRevision <= 0 {
			return executionError(errors.New("expected_revision must be positive"))
		}
		if err := validateMapThemeSnapshotShape(arguments["snapshot"], input.Snapshot); err != nil {
			return executionError(err)
		}
		theme, changed, err := tools.service.UpdateMapThemeSnapshot(ctx, input.ThemeID, input.ExpectedRevision, input.Snapshot.native())
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			Theme   mapThemeOutput `json:"theme"`
			Changed bool           `json:"changed"`
		}{projectMapTheme(theme), changed})
	case ToolMapThemeGet, ToolMapThemeDelete, ToolMapThemeCopy, ToolMapThemeSetDefault:
		var input struct {
			ThemeID string `json:"theme_id"`
			Name    string `json:"name"`
		}
		if name != ToolMapThemeCopy {
			var target struct {
				ThemeID string `json:"theme_id"`
			}
			if err := decodeArguments(arguments, &target); err != nil {
				return executionError(err)
			}
			input.ThemeID = target.ThemeID
		} else if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("theme_id", input.ThemeID); err != nil {
			return executionError(err)
		}
		switch name {
		case ToolMapThemeGet:
			response, err := tools.service.GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: input.ThemeID}))
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				Theme mapThemeOutput `json:"theme"`
			}{projectMapTheme(response.Msg)})
		case ToolMapThemeDelete:
			response, err := tools.service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: input.ThemeID}))
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				ThemeID string `json:"theme_id"`
				Success bool   `json:"success"`
			}{input.ThemeID, response.Msg.Success})
		case ToolMapThemeCopy:
			response, err := tools.service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{Id: input.ThemeID, Name: input.Name}))
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				Theme mapThemeOutput `json:"theme"`
			}{projectMapTheme(response.Msg)})
		case ToolMapThemeSetDefault:
			response, err := tools.service.SetDefaultMapTheme(ctx, connect.NewRequest(&managev1.SetDefaultMapThemeRequest{ThemeId: input.ThemeID}))
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				DefaultID string `json:"default_map_theme_id"`
			}{response.Msg.DefaultMapThemeId})
		}
	}
	return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
}

type mapThemeOutput struct {
	ID        string           `json:"id"`
	Revision  int64            `json:"revision"`
	Snapshot  mapThemeSnapshot `json:"snapshot"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
}
type mapThemeSnapshot struct {
	Name         string            `json:"name"`
	Settings     *mapThemeSettings `json:"settings"`
	LightVariant *mapThemeVariant  `json:"light_variant"`
	DarkVariant  *mapThemeVariant  `json:"dark_variant"`
}
type mapThemeSettings struct {
	CalloutScale        float64  `json:"callout_scale"`
	CalloutOffsetX      int32    `json:"callout_offset_x"`
	CalloutOffsetY      int32    `json:"callout_offset_y"`
	CalloutFields       []string `json:"callout_fields"`
	AttributionFontSize int32    `json:"attribution_font_size"`
	ShowAreaLabels      bool     `json:"show_area_labels"`
	ShowPoiLabels       bool     `json:"show_poi_labels"`
}
type mapThemeVariant struct {
	BackgroundColor              string `json:"background_color"`
	WaterColor                   string `json:"water_color"`
	LandColor                    string `json:"land_color"`
	RoadColor                    string `json:"road_color"`
	BuildingFillColor            string `json:"building_fill_color"`
	BuildingStrokeEnabled        bool   `json:"building_stroke_enabled"`
	BuildingStrokeColor          string `json:"building_stroke_color"`
	CalloutLineColor             string `json:"callout_line_color"`
	CalloutTextColor             string `json:"callout_text_color"`
	CalloutBackgroundColor       string `json:"callout_background_color"`
	CalloutDescriptionColor      string `json:"callout_description_color"`
	AttributionColor             string `json:"attribution_color"`
	LabelTextColor               string `json:"label_text_color"`
	ClusterColor                 string `json:"cluster_color"`
	ClusterHoverColor            string `json:"cluster_hover_color"`
	ClusterTextColor             string `json:"cluster_text_color"`
	ClusterTextHoverColor        string `json:"cluster_text_hover_color"`
	CalloutHoverLineColor        string `json:"callout_hover_line_color"`
	CalloutHoverTextColor        string `json:"callout_hover_text_color"`
	CalloutHoverDescriptionColor string `json:"callout_hover_description_color"`
	CalloutHoverBackgroundColor  string `json:"callout_hover_background_color"`
}

func (snapshot mapThemeSnapshot) native() *managev1.CreateMapThemeRequest {
	return &managev1.CreateMapThemeRequest{Name: snapshot.Name, Settings: snapshot.Settings.native(), LightVariant: snapshot.LightVariant.native(), DarkVariant: snapshot.DarkVariant.native()}
}
func (input mapThemeSettings) native() *managev1.MapThemeSettings {
	return &managev1.MapThemeSettings{
		CalloutScale:        input.CalloutScale,
		CalloutOffsetX:      input.CalloutOffsetX,
		CalloutOffsetY:      input.CalloutOffsetY,
		CalloutFields:       input.CalloutFields,
		AttributionFontSize: input.AttributionFontSize,
		ShowAreaLabels:      input.ShowAreaLabels,
		ShowPoiLabels:       input.ShowPoiLabels,
	}
}
func (input mapThemeVariant) native() *managev1.MapThemeVariantInput {
	return &managev1.MapThemeVariantInput{
		BackgroundColor:              input.BackgroundColor,
		WaterColor:                   input.WaterColor,
		LandColor:                    input.LandColor,
		RoadColor:                    input.RoadColor,
		BuildingFillColor:            input.BuildingFillColor,
		BuildingStrokeEnabled:        input.BuildingStrokeEnabled,
		BuildingStrokeColor:          input.BuildingStrokeColor,
		CalloutLineColor:             input.CalloutLineColor,
		CalloutTextColor:             input.CalloutTextColor,
		CalloutBackgroundColor:       input.CalloutBackgroundColor,
		CalloutDescriptionColor:      input.CalloutDescriptionColor,
		AttributionColor:             input.AttributionColor,
		LabelTextColor:               input.LabelTextColor,
		ClusterColor:                 input.ClusterColor,
		ClusterHoverColor:            input.ClusterHoverColor,
		ClusterTextColor:             input.ClusterTextColor,
		ClusterTextHoverColor:        input.ClusterTextHoverColor,
		CalloutHoverLineColor:        input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}
func projectMapThemeSettings(input *managev1.MapThemeSettings) mapThemeSettings {
	return mapThemeSettings{
		CalloutScale:        input.GetCalloutScale(),
		CalloutOffsetX:      input.GetCalloutOffsetX(),
		CalloutOffsetY:      input.GetCalloutOffsetY(),
		CalloutFields:       input.GetCalloutFields(),
		AttributionFontSize: input.GetAttributionFontSize(),
		ShowAreaLabels:      input.GetShowAreaLabels(),
		ShowPoiLabels:       input.GetShowPoiLabels(),
	}
}
func projectMapThemeVariant(input *managev1.MapThemeVariant) mapThemeVariant {
	return mapThemeVariant{
		BackgroundColor:              input.GetBackgroundColor(),
		WaterColor:                   input.GetWaterColor(),
		LandColor:                    input.GetLandColor(),
		RoadColor:                    input.GetRoadColor(),
		BuildingFillColor:            input.GetBuildingFillColor(),
		BuildingStrokeEnabled:        input.GetBuildingStrokeEnabled(),
		BuildingStrokeColor:          input.GetBuildingStrokeColor(),
		CalloutLineColor:             input.GetCalloutLineColor(),
		CalloutTextColor:             input.GetCalloutTextColor(),
		CalloutBackgroundColor:       input.GetCalloutBackgroundColor(),
		CalloutDescriptionColor:      input.GetCalloutDescriptionColor(),
		AttributionColor:             input.GetAttributionColor(),
		LabelTextColor:               input.GetLabelTextColor(),
		ClusterColor:                 input.GetClusterColor(),
		ClusterHoverColor:            input.GetClusterHoverColor(),
		ClusterTextColor:             input.GetClusterTextColor(),
		ClusterTextHoverColor:        input.GetClusterTextHoverColor(),
		CalloutHoverLineColor:        input.GetCalloutHoverLineColor(),
		CalloutHoverTextColor:        input.GetCalloutHoverTextColor(),
		CalloutHoverDescriptionColor: input.GetCalloutHoverDescriptionColor(),
		CalloutHoverBackgroundColor:  input.GetCalloutHoverBackgroundColor(),
	}
}

func projectMapTheme(theme *managev1.MapTheme) mapThemeOutput {
	settings, light, dark := projectMapThemeSettings(theme.GetSettings()), projectMapThemeVariant(theme.GetLightVariant()), projectMapThemeVariant(theme.GetDarkVariant())
	return mapThemeOutput{ID: theme.GetId(), Revision: theme.GetRevision(), Snapshot: mapThemeSnapshot{Name: theme.GetName(), Settings: &settings, LightVariant: &light, DarkVariant: &dark}, CreatedAt: timestampString(theme.GetCreatedAt()), UpdatedAt: timestampString(theme.GetUpdatedAt())}
}

// Native validation owns values and bounds. This shape check prevents JSON null
// scalar fields from silently becoming zero/false in a complete snapshot.
func validateMapThemeSnapshotShape(raw json.RawMessage, snapshot *mapThemeSnapshot) error {
	if snapshot == nil || snapshot.Settings == nil || snapshot.LightVariant == nil || snapshot.DarkVariant == nil {
		return errors.New("snapshot requires settings, light_variant, and dark_variant")
	}
	var objects struct {
		Settings mcpserver.ToolArguments `json:"settings"`
		Light    mcpserver.ToolArguments `json:"light_variant"`
		Dark     mcpserver.ToolArguments `json:"dark_variant"`
	}
	if err := json.Unmarshal(raw, &objects); err != nil {
		return err
	}
	for _, fields := range []mcpserver.ToolArguments{objects.Settings, objects.Light, objects.Dark} {
		for field := range fields {
			if err := rejectNullArguments(fields, field); err != nil {
				return err
			}
		}
	}
	return nil
}
func mapThemeResult(value any) (mcpserver.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP Map Theme result: %w", err)
	}
	return structuredResult(encoded, false)
}
