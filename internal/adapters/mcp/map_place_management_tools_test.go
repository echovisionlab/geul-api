package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/stretchr/testify/require"
)

const mapPlaceManagementTestID = "11111111-1111-4111-8111-111111111111"
const mapPlaceManagementSecondID = "22222222-2222-4222-8222-222222222222"

func TestMapPlaceManagementDescriptors(t *testing.T) {
	_, err := NewMapPlaceManagementTools(nil)
	require.Error(t, err)
	var missing *recordingMapPlaceManagement
	_, err = NewMapPlaceManagementTools(missing)
	require.Error(t, err)
	tools, err := NewMapPlaceManagementTools(&recordingMapPlaceManagement{})
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Equal(t, []string{ToolMapPlaceGet, ToolMapPlaceGetMany, ToolMapPlaceList, ToolMapPlaceCreate, ToolMapPlaceSettingsUpdate, ToolMapPlaceDelete}, tools.ToolNames())
	for i, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, i < 3, tool.Annotations["readOnlyHint"])
		require.Equal(t, tool.Name == ToolMapPlaceDelete, tool.Annotations["destructiveHint"])
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			require.NoError(t, json.Unmarshal(schema, &object))
			require.Equal(t, "object", object["type"])
		}
	}
	listed[0].Annotations["readOnlyHint"] = false
	listedAgain, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Equal(t, true, listedAgain[0].Annotations["readOnlyHint"])
}

func TestMapPlaceManagementReadProjectionAndOrder(t *testing.T) {
	image, google, city := mapPlaceManagementSecondID, "google-id", "Seoul"
	place := &managev1.MapPlace{Id: mapPlaceManagementTestID, Name: "Park", Address: "1 Park Road", Lat: 0, Lng: 0, AddressComponents: &managev1.AddressComponents{City: &city}, ImageFileId: &image, GooglePlaceId: &google, ImageAsset: &commonv1.AssetRef{Url: "https://cdn.example.test/map.png"}}
	app := &recordingMapPlaceManagement{place: place, places: []*managev1.MapPlace{place}}
	tools, err := NewMapPlaceManagementTools(app)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceGet, toolArguments(t, `{"map_place_id":"`+mapPlaceManagementTestID+`"}`))
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, t.Context(), app.ctx)
	require.Equal(t, mapPlaceManagementTestID, app.get.Id)
	projected := result.StructuredContent["place"].(map[string]any)
	require.Len(t, projected, 9)
	require.Equal(t, float64(0), projected["lat"])
	require.Equal(t, "https://cdn.example.test/map.png", projected["image_url"])
	require.Equal(t, map[string]any{"city": "Seoul"}, projected["address_components"])
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceGetMany, toolArguments(t, `{"map_place_ids":["`+mapPlaceManagementTestID+`","`+mapPlaceManagementSecondID+`"]}`))
	require.NoError(t, err)
	require.Equal(t, []string{mapPlaceManagementTestID, mapPlaceManagementSecondID}, app.many.Ids)
	require.Len(t, result.StructuredContent["items"], 1)
	app.places = nil
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceGetMany, toolArguments(t, `{"map_place_ids":["`+mapPlaceManagementTestID+`"]}`))
	require.NoError(t, err)
	require.Equal(t, []any{}, result.StructuredContent["items"])
}

func TestMapPlaceManagementListQueryAndContinuation(t *testing.T) {
	for _, test := range []struct {
		args                 string
		total, limit, offset int32
		more                 bool
		query                string
	}{
		{`{}`, 21, 20, 0, true, ""},
		{`{"query":"  park  ","limit":10,"offset":10}`, 20, 10, 10, false, "park"},
		{`{"limit":1,"offset":2147483647}`, 0, 1, 2147483647, false, ""},
	} {
		t.Run(test.args, func(t *testing.T) {
			app := &recordingMapPlaceManagement{pagination: &commonv1.PaginationResponse{Total: test.total, Limit: test.limit, Offset: test.offset}}
			tools, err := NewMapPlaceManagementTools(app)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceList, toolArguments(t, test.args))
			require.NoError(t, err)
			require.Equal(t, test.limit, app.list.Pagination.Limit)
			require.Equal(t, test.offset, app.list.Pagination.Offset)
			require.Empty(t, app.list.Sorts)
			if test.query == "" {
				require.Empty(t, app.list.Filters)
			} else {
				require.Equal(t, []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: test.query}}, app.list.Filters)
			}
			require.Equal(t, test.more, result.StructuredContent["has_more"])
			if test.more {
				require.Equal(t, float64(int64(test.offset)+int64(test.limit)), result.StructuredContent["next_offset"])
			} else {
				require.NotContains(t, result.StructuredContent, "next_offset")
			}
		})
	}
}

func TestMapPlaceManagementMutationsPreserveOptionalValues(t *testing.T) {
	app := &recordingMapPlaceManagement{}
	tools, err := NewMapPlaceManagementTools(app)
	require.NoError(t, err)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceCreate, toolArguments(t, `{"name":"Park","address":"1 Park Road","lat":0,"lng":0,"address_components":{"city":"Seoul","postal_code":""},"image_file_id":"`+mapPlaceManagementSecondID+`","google_place_id":"google-id"}`))
	require.NoError(t, err)
	require.Equal(t, float64(0), app.create.Lat)
	require.Equal(t, float64(0), app.create.Lng)
	require.Equal(t, "", *app.create.AddressComponents.PostalCode)
	require.Equal(t, mapPlaceManagementSecondID, *app.create.ImageFileId)
	require.Equal(t, "google-id", *app.create.GooglePlaceId)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceSettingsUpdate, toolArguments(t, `{"map_place_id":"`+mapPlaceManagementTestID+`","lat":0,"lng":0,"address_components":{},"image_file_id":"`+mapPlaceManagementSecondID+`","clear_image":true,"google_place_id":""}`))
	require.NoError(t, err)
	require.Nil(t, app.update.Name)
	require.Nil(t, app.update.Address)
	require.NotNil(t, app.update.Lat)
	require.Equal(t, float64(0), *app.update.Lat)
	require.NotNil(t, app.update.Lng)
	require.Equal(t, float64(0), *app.update.Lng)
	require.NotNil(t, app.update.AddressComponents)
	require.Nil(t, app.update.AddressComponents.City)
	require.True(t, app.update.ClearImage)
	require.Equal(t, mapPlaceManagementSecondID, *app.update.ImageFileId)
	require.NotNil(t, app.update.GooglePlaceId)
	require.Empty(t, *app.update.GooglePlaceId)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceSettingsUpdate, toolArguments(t, `{"map_place_id":"`+mapPlaceManagementTestID+`"}`))
	require.NoError(t, err)
	require.Nil(t, app.update.Lat)
	require.Nil(t, app.update.Lng)
	require.Nil(t, app.update.AddressComponents)
	require.Nil(t, app.update.GooglePlaceId)
	require.Nil(t, app.update.ImageFileId)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMapPlaceDelete, toolArguments(t, `{"map_place_id":"`+mapPlaceManagementTestID+`"}`))
	require.NoError(t, err)
	require.Equal(t, mapPlaceManagementTestID, app.delete.Id)
	require.Equal(t, true, result.StructuredContent["success"])
}

func TestMapPlaceManagementRejectsInvalidArgumentsBeforeOwner(t *testing.T) {
	for _, test := range []struct{ name, args string }{
		{ToolMapPlaceGet, `{"map_place_id":"park"}`},
		{ToolMapPlaceGetMany, `{"map_place_ids":[]}`},
		{ToolMapPlaceGetMany, `{"map_place_ids":["invalid"]}`},
		{ToolMapPlaceList, `{"status":"published"}`},
		{ToolMapPlaceList, `{"limit":0}`}, {ToolMapPlaceList, `{"limit":101}`}, {ToolMapPlaceList, `{"offset":-1}`}, {ToolMapPlaceList, `{"offset":2147483648}`}, {ToolMapPlaceList, `{"query":null}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":0}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":null,"lng":0}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":91,"lng":0}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":0,"lng":181}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":0,"lng":0,"clear_image":false}`},
		{ToolMapPlaceCreate, `{"name":"Park","address":"Road","lat":0,"lng":0,"map_place_id":"` + mapPlaceManagementTestID + `"}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","name":" "}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","address":""}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","image_file_id":""}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","address_components":null}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","address_components":{"city":null}}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","address_components":{"district":"x"}}`},
		{ToolMapPlaceSettingsUpdate, `{"map_place_id":"` + mapPlaceManagementTestID + `","locale":"ko"}`},
	} {
		t.Run(test.name+test.args, func(t *testing.T) {
			app := &recordingMapPlaceManagement{}
			tools, err := NewMapPlaceManagementTools(app)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.args))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Zero(t, app.calls)
		})
	}
}

func TestMapPlaceManagementPreservesNativeErrors(t *testing.T) {
	for _, name := range []string{ToolMapPlaceGet, ToolMapPlaceGetMany, ToolMapPlaceList, ToolMapPlaceCreate, ToolMapPlaceSettingsUpdate, ToolMapPlaceDelete} {
		t.Run(name, func(t *testing.T) {
			app := &recordingMapPlaceManagement{err: connect.NewError(connect.CodePermissionDenied, context.Canceled)}
			tools, err := NewMapPlaceManagementTools(app)
			require.NoError(t, err)
			args := `{"map_place_id":"` + mapPlaceManagementTestID + `"}`
			switch name {
			case ToolMapPlaceGetMany:
				args = `{"map_place_ids":["` + mapPlaceManagementTestID + `"]}`
			case ToolMapPlaceList:
				args = `{}`
			case ToolMapPlaceCreate:
				args = `{"name":"Park","address":"Road","lat":0,"lng":0}`
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, args))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Equal(t, 1, app.calls)
		})
	}
	tools, err := NewMapPlaceManagementTools(&recordingMapPlaceManagement{})
	require.NoError(t, err)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", nil)
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
}

type recordingMapPlaceManagement struct {
	managev1connect.UnimplementedMapPlaceServiceHandler
	ctx        context.Context
	calls      int
	err        error
	place      *managev1.MapPlace
	places     []*managev1.MapPlace
	pagination *commonv1.PaginationResponse
	get        *managev1.GetMapPlaceRequest
	many       *managev1.GetMapPlacesByIdsRequest
	list       *managev1.ListMapPlacesAdminRequest
	create     *managev1.CreateMapPlaceRequest
	update     *managev1.UpdateMapPlaceRequest
	delete     *managev1.DeleteMapPlaceRequest
}

func (app *recordingMapPlaceManagement) record(ctx context.Context) *managev1.MapPlace {
	app.ctx = ctx
	app.calls++
	if app.place != nil {
		return app.place
	}
	return &managev1.MapPlace{Id: mapPlaceManagementTestID}
}
func (app *recordingMapPlaceManagement) GetMapPlace(ctx context.Context, req *connect.Request[managev1.GetMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	app.get = req.Msg
	return connect.NewResponse(app.record(ctx)), app.err
}
func (app *recordingMapPlaceManagement) GetMapPlacesByIds(ctx context.Context, req *connect.Request[managev1.GetMapPlacesByIdsRequest]) (*connect.Response[managev1.GetMapPlacesByIdsResponse], error) {
	app.many = req.Msg
	app.record(ctx)
	return connect.NewResponse(&managev1.GetMapPlacesByIdsResponse{Places: app.places}), app.err
}
func (app *recordingMapPlaceManagement) ListMapPlacesAdmin(ctx context.Context, req *connect.Request[managev1.ListMapPlacesAdminRequest]) (*connect.Response[managev1.ListMapPlacesAdminResponse], error) {
	app.list = req.Msg
	app.record(ctx)
	return connect.NewResponse(&managev1.ListMapPlacesAdminResponse{Places: app.places, Pagination: app.pagination}), app.err
}
func (app *recordingMapPlaceManagement) CreateMapPlace(ctx context.Context, req *connect.Request[managev1.CreateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	app.create = req.Msg
	return connect.NewResponse(app.record(ctx)), app.err
}
func (app *recordingMapPlaceManagement) UpdateMapPlace(ctx context.Context, req *connect.Request[managev1.UpdateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error) {
	app.update = req.Msg
	return connect.NewResponse(app.record(ctx)), app.err
}
func (app *recordingMapPlaceManagement) DeleteMapPlace(ctx context.Context, req *connect.Request[managev1.DeleteMapPlaceRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	app.delete = req.Msg
	app.record(ctx)
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), app.err
}
