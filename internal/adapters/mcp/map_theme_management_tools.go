package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/maptheme"
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

// MapThemeManagement consumes the administrator Map Theme use cases;
// authorization and request-actor audit remain in maptheme.Service, outside
// collaboration saves.
type MapThemeManagement interface {
	ListMapThemes(context.Context) (maptheme.ThemeList, error)
	ResolveMapTheme(context.Context, maptheme.ResolveMapThemeQuery) (*managev1.ResolvedMapTheme, error)
	GetMapTheme(context.Context, maptheme.GetMapThemeQuery) (*managev1.MapTheme, error)
	CreateMapTheme(context.Context, maptheme.CreateMapThemeCommand) (*managev1.MapTheme, error)
	DeleteMapTheme(context.Context, maptheme.DeleteMapThemeCommand) error
	CopyMapTheme(context.Context, maptheme.CopyMapThemeCommand) (*managev1.MapTheme, error)
	SetDefaultMapTheme(context.Context, maptheme.SetDefaultMapThemeCommand) (string, error)
	UpdateMapThemeSnapshot(context.Context, maptheme.UpdateMapThemeSnapshotCommand) (maptheme.SnapshotUpdate, error)
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
		list, err := tools.service.ListMapThemes(ctx)
		if err != nil {
			return expectedToolError(err)
		}
		themes := make([]mapThemeOutput, 0, len(list.Themes))
		for _, theme := range list.Themes {
			themes = append(themes, projectMapTheme(theme))
		}
		return mapThemeResult(struct {
			Themes    []mapThemeOutput `json:"themes"`
			DefaultID string           `json:"default_map_theme_id"`
		}{themes, list.DefaultID})
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
		resolved, err := tools.service.ResolveMapTheme(ctx, maptheme.ResolveMapThemeQuery{ThemeID: input.ThemeID, Scheme: input.Scheme})
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			ThemeID  string           `json:"theme_id"`
			Scheme   string           `json:"scheme"`
			Settings mapThemeSettings `json:"settings"`
			Variant  mapThemeVariant  `json:"variant"`
		}{resolved.ThemeId, resolved.Scheme, projectMapThemeSettings(resolved.Settings), projectMapThemeVariant(resolved.Variant)})
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
		theme, err := tools.service.CreateMapTheme(ctx, maptheme.CreateMapThemeCommand{Snapshot: *snapshot.native()})
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			Theme mapThemeOutput `json:"theme"`
		}{projectMapTheme(theme)})
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
		update, err := tools.service.UpdateMapThemeSnapshot(ctx, maptheme.UpdateMapThemeSnapshotCommand{
			ThemeID: input.ThemeID, ExpectedRevision: input.ExpectedRevision, Snapshot: input.Snapshot.native(),
		})
		if err != nil {
			return expectedToolError(err)
		}
		return mapThemeResult(struct {
			Theme   mapThemeOutput `json:"theme"`
			Changed bool           `json:"changed"`
		}{projectMapTheme(update.Theme), update.Changed})
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
			theme, err := tools.service.GetMapTheme(ctx, maptheme.GetMapThemeQuery{ID: input.ThemeID})
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				Theme mapThemeOutput `json:"theme"`
			}{projectMapTheme(theme)})
		case ToolMapThemeDelete:
			if err := tools.service.DeleteMapTheme(ctx, maptheme.DeleteMapThemeCommand{ID: input.ThemeID}); err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				ThemeID string `json:"theme_id"`
				Success bool   `json:"success"`
			}{input.ThemeID, true})
		case ToolMapThemeCopy:
			theme, err := tools.service.CopyMapTheme(ctx, maptheme.CopyMapThemeCommand{ID: input.ThemeID, Name: input.Name})
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				Theme mapThemeOutput `json:"theme"`
			}{projectMapTheme(theme)})
		case ToolMapThemeSetDefault:
			defaultID, err := tools.service.SetDefaultMapTheme(ctx, maptheme.SetDefaultMapThemeCommand{ThemeID: input.ThemeID})
			if err != nil {
				return expectedToolError(err)
			}
			return mapThemeResult(struct {
				DefaultID string `json:"default_map_theme_id"`
			}{defaultID})
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

// native is the use-case input of snapshot; the MCP settings and variant
// shapes are the use-case types with JSON names.
func (snapshot mapThemeSnapshot) native() *maptheme.SnapshotInput {
	return &maptheme.SnapshotInput{
		Name: snapshot.Name, Settings: (*maptheme.Settings)(snapshot.Settings),
		LightVariant: (*maptheme.Variant)(snapshot.LightVariant), DarkVariant: (*maptheme.Variant)(snapshot.DarkVariant),
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
