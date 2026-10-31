package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const mapThemeTestID = "11111111-1111-4111-8111-111111111111"

func TestMapThemeManagementDescriptors(t *testing.T) {
	var missing *recordingMapThemeManagement
	for _, service := range []MapThemeManagement{nil, missing} {
		_, err := NewMapThemeManagementTools(service)
		require.Error(t, err)
	}
	tools, err := NewMapThemeManagementTools(&recordingMapThemeManagement{})
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 8)
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, tool.Name == ToolMapThemeList || tool.Name == ToolMapThemeGet || tool.Name == ToolMapThemeResolve, tool.Annotations["readOnlyHint"])
		require.Equal(t, tool.Name == ToolMapThemeDelete, tool.Annotations["destructiveHint"])
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(raw, &schema))
			require.Equal(t, "object", schema["type"])
			require.Equal(t, false, schema["additionalProperties"])
		}
	}
}

func TestMapThemeManagementCallsExactOwningMethods(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments map[string]any
	}{
		{ToolMapThemeList, map[string]any{}},
		{ToolMapThemeResolve, map[string]any{}},
		{ToolMapThemeResolve, map[string]any{"theme_id": mapThemeTestID, "scheme": "dark"}},
		{ToolMapThemeGet, map[string]any{"theme_id": mapThemeTestID}},
		{ToolMapThemeCreate, mapThemeSnapshotArgumentsForTest(t)},
		{ToolMapThemeDelete, map[string]any{"theme_id": mapThemeTestID}},
		{ToolMapThemeCopy, map[string]any{"theme_id": mapThemeTestID, "name": "Copied theme"}},
		{ToolMapThemeSetDefault, map[string]any{"theme_id": mapThemeTestID}},
		{ToolMapThemeSettingsUpdate, map[string]any{"theme_id": mapThemeTestID, "expected_revision": int64(7), "snapshot": mapThemeSnapshotArgumentsForTest(t)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingMapThemeManagement{theme: mapThemeForTest(t), changed: true}
			tools, err := NewMapThemeManagementTools(service)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, mapThemeArgumentsForTest(t, test.arguments))
			require.NoError(t, err)
			require.Equal(t, test.name, service.called)
			require.Equal(t, t.Context(), service.ctx)
			switch test.name {
			case ToolMapThemeResolve:
				request := service.request.(maptheme.ResolveMapThemeQuery)
				if test.arguments["theme_id"] == nil {
					require.Nil(t, request.ThemeID)
					require.Equal(t, "light", request.Scheme)
				} else {
					require.Equal(t, mapThemeTestID, *request.ThemeID)
					require.Equal(t, "dark", request.Scheme)
				}
			case ToolMapThemeCreate:
				require.Equal(t, maptheme.CreateMapThemeCommand{Snapshot: *mapThemeSnapshotInputForTest()}, service.request)
			case ToolMapThemeSettingsUpdate:
				require.Equal(t, maptheme.UpdateMapThemeSnapshotCommand{ThemeID: mapThemeTestID, ExpectedRevision: 7, Snapshot: mapThemeSnapshotInputForTest()}, service.request)
				require.Equal(t, true, result.StructuredContent["changed"])
			case ToolMapThemeGet:
				require.Equal(t, maptheme.GetMapThemeQuery{ID: mapThemeTestID}, service.request)
			case ToolMapThemeDelete:
				require.Equal(t, maptheme.DeleteMapThemeCommand{ID: mapThemeTestID}, service.request)
			case ToolMapThemeCopy:
				require.Equal(t, maptheme.CopyMapThemeCommand{ID: mapThemeTestID, Name: "Copied theme"}, service.request)
			case ToolMapThemeSetDefault:
				require.Equal(t, maptheme.SetDefaultMapThemeCommand{ThemeID: mapThemeTestID}, service.request)
			}
		})
	}
}

func TestMapThemeManagementGetSnapshotRoundtripsFalseAndZero(t *testing.T) {
	service := &recordingMapThemeManagement{theme: mapThemeForTest(t)}
	tools, err := NewMapThemeManagementTools(service)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapThemeGet, mapThemeArgumentsForTest(t, map[string]any{"theme_id": mapThemeTestID}))
	require.NoError(t, err)
	theme := result.StructuredContent["theme"].(map[string]any)
	require.Equal(t, float64(7), theme["revision"])
	snapshot := theme["snapshot"].(map[string]any)
	settings := snapshot["settings"].(map[string]any)
	require.Equal(t, false, settings["show_area_labels"])
	require.Equal(t, float64(0), settings["callout_offset_x"])
	require.Equal(t, false, snapshot["light_variant"].(map[string]any)["building_stroke_enabled"])
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapThemeSettingsUpdate, mapThemeArgumentsForTest(t, map[string]any{"theme_id": mapThemeTestID, "expected_revision": theme["revision"], "snapshot": snapshot}))
	require.NoError(t, err)
	require.False(t, result.StructuredContent["changed"].(bool))
	require.Equal(t, mapThemeSnapshotInputForTest(), service.request.(maptheme.UpdateMapThemeSnapshotCommand).Snapshot)
}

func TestMapThemeManagementRejectsInvalidShapeBeforeOwner(t *testing.T) {
	for _, test := range []struct{ name, arguments string }{
		{ToolMapThemeList, `{"theme_id":"` + mapThemeTestID + `"}`},
		{ToolMapThemeGet, `{"theme_id":"THEME"}`},
		{ToolMapThemeDelete, `{"theme_id":"11111111111141118111111111111111"}`},
		{ToolMapThemeGet, `{"theme_id":"` + mapThemeTestID + `","name":"extra"}`},
		{ToolMapThemeResolve, `{"theme_id":null}`}, {ToolMapThemeResolve, `{"scheme":null}`}, {ToolMapThemeResolve, `{"scheme":""}`}, {ToolMapThemeResolve, `{"scheme":"auto"}`},
		{ToolMapThemeSettingsUpdate, `{"theme_id":"` + mapThemeTestID + `","expected_revision":0}`},
		{ToolMapThemeSettingsUpdate, `{"theme_id":"` + mapThemeTestID + `","expected_revision":null}`},
		{ToolMapThemeSettingsUpdate, `{"theme_id":"` + mapThemeTestID + `","expected_revision":9223372036854775808}`},
		{ToolMapThemeSettingsUpdate, `{"theme_id":"` + mapThemeTestID + `","expected_revision":1,"snapshot":null}`},
		{ToolMapThemeCreate, `{"name":"Missing snapshot fields"}`},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			service := &recordingMapThemeManagement{}
			tools, err := NewMapThemeManagementTools(service)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Empty(t, service.called)
		})
	}
	for _, field := range []string{"show_area_labels", "callout_offset_x", "callout_fields"} {
		arguments := mapThemeSnapshotArgumentsForTest(t)
		arguments["settings"].(map[string]any)[field] = nil
		service := &recordingMapThemeManagement{}
		tools, err := NewMapThemeManagementTools(service)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapThemeCreate, mapThemeArgumentsForTest(t, arguments))
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution)
		require.Empty(t, service.called)
	}
}

func TestMapThemeManagementPreservesOwningFailures(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeInvalidArgument, connect.CodeNotFound} {
		service := &recordingMapThemeManagement{err: connect.NewError(code, errors.New("owning policy outcome"))}
		tools, err := NewMapThemeManagementTools(service)
		require.NoError(t, err)
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapThemeDelete, mapThemeArgumentsForTest(t, map[string]any{"theme_id": mapThemeTestID}))
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution)
		require.Equal(t, "owning policy outcome", execution.Message)
		require.Nil(t, result.StructuredContent)
	}
}

func mapThemeNativeSnapshotForTest() *managev1.CreateMapThemeRequest {
	variant := &managev1.MapThemeVariantInput{BackgroundColor: "#fff", WaterColor: "#111", LandColor: "#222", RoadColor: "#333", BuildingFillColor: "#444", BuildingStrokeColor: "#555", CalloutLineColor: "#666", CalloutTextColor: "#777", CalloutBackgroundColor: "#888", CalloutDescriptionColor: "#999", AttributionColor: "transparent", LabelTextColor: "#aaa", ClusterColor: "#bbb", ClusterHoverColor: "#ccc", ClusterTextColor: "#ddd", ClusterTextHoverColor: "#eee", CalloutHoverLineColor: "#123", CalloutHoverTextColor: "#456", CalloutHoverDescriptionColor: "#789", CalloutHoverBackgroundColor: "#abc"}
	return &managev1.CreateMapThemeRequest{Name: "Theme", Settings: &managev1.MapThemeSettings{CalloutScale: 1.25, CalloutFields: []string{"name", "address"}, AttributionFontSize: 12}, LightVariant: variant, DarkVariant: proto.Clone(variant).(*managev1.MapThemeVariantInput)}
}

func mapThemeSnapshotArgumentsForTest(t *testing.T) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(mapThemeNativeSnapshotForTest())
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(encoded, &result))
	return result
}

func mapThemeArgumentsForTest(t *testing.T, input map[string]any) mcpserver.ToolArguments {
	t.Helper()
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	return toolArguments(t, string(encoded))
}

func mapThemeForTest(t *testing.T) *managev1.MapTheme {
	t.Helper()
	snapshot := mapThemeNativeSnapshotForTest()
	variant := &managev1.MapThemeVariant{}
	encoded, err := json.Marshal(snapshot.LightVariant)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, variant))
	when := timestamppb.New(time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC))
	return &managev1.MapTheme{Id: mapThemeTestID, Name: snapshot.Name, Revision: 7, Settings: snapshot.Settings, LightVariant: variant, DarkVariant: proto.Clone(variant).(*managev1.MapThemeVariant), CreatedAt: when, UpdatedAt: when}
}

// mapThemeSnapshotInputForTest is mapThemeNativeSnapshotForTest as the use
// case receives it.
func mapThemeSnapshotInputForTest() *maptheme.SnapshotInput {
	variant := maptheme.Variant{BackgroundColor: "#fff", WaterColor: "#111", LandColor: "#222", RoadColor: "#333", BuildingFillColor: "#444", BuildingStrokeColor: "#555", CalloutLineColor: "#666", CalloutTextColor: "#777", CalloutBackgroundColor: "#888", CalloutDescriptionColor: "#999", AttributionColor: "transparent", LabelTextColor: "#aaa", ClusterColor: "#bbb", ClusterHoverColor: "#ccc", ClusterTextColor: "#ddd", ClusterTextHoverColor: "#eee", CalloutHoverLineColor: "#123", CalloutHoverTextColor: "#456", CalloutHoverDescriptionColor: "#789", CalloutHoverBackgroundColor: "#abc"}
	dark := variant
	return &maptheme.SnapshotInput{Name: "Theme", Settings: &maptheme.Settings{CalloutScale: 1.25, CalloutFields: []string{"name", "address"}, AttributionFontSize: 12}, LightVariant: &variant, DarkVariant: &dark}
}

type recordingMapThemeManagement struct {
	theme   *managev1.MapTheme
	err     error
	changed bool
	called  string
	request any
	ctx     context.Context
}

func (service *recordingMapThemeManagement) record(ctx context.Context, name string, request any) {
	service.ctx, service.called, service.request = ctx, name, request
}

func (service *recordingMapThemeManagement) ListMapThemes(ctx context.Context) (maptheme.ThemeList, error) {
	service.record(ctx, ToolMapThemeList, nil)
	return maptheme.ThemeList{Themes: []*managev1.MapTheme{service.theme}, DefaultID: mapThemeTestID}, service.err
}

func (service *recordingMapThemeManagement) ResolveMapTheme(ctx context.Context, query maptheme.ResolveMapThemeQuery) (*managev1.ResolvedMapTheme, error) {
	service.record(ctx, ToolMapThemeResolve, query)
	return &managev1.ResolvedMapTheme{ThemeId: mapThemeTestID, Scheme: query.Scheme, Settings: service.theme.GetSettings(), Variant: service.theme.GetLightVariant()}, service.err
}

func (service *recordingMapThemeManagement) GetMapTheme(ctx context.Context, query maptheme.GetMapThemeQuery) (*managev1.MapTheme, error) {
	service.record(ctx, ToolMapThemeGet, query)
	return service.theme, service.err
}

func (service *recordingMapThemeManagement) CreateMapTheme(ctx context.Context, command maptheme.CreateMapThemeCommand) (*managev1.MapTheme, error) {
	service.record(ctx, ToolMapThemeCreate, command)
	return service.theme, service.err
}

func (service *recordingMapThemeManagement) CopyMapTheme(ctx context.Context, command maptheme.CopyMapThemeCommand) (*managev1.MapTheme, error) {
	service.record(ctx, ToolMapThemeCopy, command)
	return service.theme, service.err
}

func (service *recordingMapThemeManagement) DeleteMapTheme(ctx context.Context, command maptheme.DeleteMapThemeCommand) error {
	service.record(ctx, ToolMapThemeDelete, command)
	return service.err
}

func (service *recordingMapThemeManagement) SetDefaultMapTheme(ctx context.Context, command maptheme.SetDefaultMapThemeCommand) (string, error) {
	service.record(ctx, ToolMapThemeSetDefault, command)
	return mapThemeTestID, service.err
}

func (service *recordingMapThemeManagement) UpdateMapThemeSnapshot(ctx context.Context, command maptheme.UpdateMapThemeSnapshotCommand) (maptheme.SnapshotUpdate, error) {
	service.record(ctx, ToolMapThemeSettingsUpdate, command)
	return maptheme.SnapshotUpdate{Theme: service.theme, Changed: service.changed}, service.err
}
