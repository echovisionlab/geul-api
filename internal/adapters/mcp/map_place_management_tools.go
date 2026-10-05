package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolMapPlaceGet            = "map_place_get"
	ToolMapPlaceGetMany        = "map_place_get_many"
	ToolMapPlaceList           = "map_place_list"
	ToolMapPlaceCreate         = "map_place_create"
	ToolMapPlaceSettingsUpdate = "map_place_settings_update"
	ToolMapPlaceDelete         = "map_place_delete"
)

var mapPlaceManagementTools = []mcpserver.Tool{
	oauthTool(ToolMapPlaceGet, "Get map place", "Read a map place's editable settings by canonical UUID. Use its id as map_place_id in content relations.", mapPlaceGetInputJSONSchema, mapPlaceGetOutputJSONSchema, true, false),
	oauthTool(ToolMapPlaceGetMany, "Get map places", "Read up to 100 map places in the requested order. Missing IDs are omitted by the native service.", mapPlaceGetManyInputJSONSchema, mapPlaceGetManyOutputJSONSchema, true, false),
	oauthTool(ToolMapPlaceList, "List map places", "List or search map places by name or address with current administrator permission. Continue with next_offset and the same query.", mapPlaceListInputJSONSchema, mapPlaceListOutputJSONSchema, true, false),
	oauthTool(ToolMapPlaceCreate, "Create map place", "Create a place with name, address, and coordinates. Optional image_file_id binds a ready map image through the owning service. Requires current author permission.", mapPlaceCreateInputJSONSchema, mapPlaceGetOutputJSONSchema, false, false),
	oauthTool(ToolMapPlaceSettingsUpdate, "Update map place settings", "Update supplied place fields with current edit permission; omitted fields stay unchanged. Coordinates may be zero. address_components replaces the supplied address components object, including an empty object. clear_image removes the image and takes precedence over image_file_id. Empty google_place_id clears the Google Places link.", mapPlaceSettingsUpdateInputJSONSchema, mapPlaceGetOutputJSONSchema, false, false),
	oauthTool(ToolMapPlaceDelete, "Delete map place", "Delete a map place with current administrator permission. The owning service rejects places referenced by content and releases image bindings.", mapPlaceGetInputJSONSchema, mapPlaceDeleteOutputJSONSchema, false, true),
}

// MapPlaceManagementApplication keeps authorization, image bindings, references,
// and mutation audit in the native Map Place service.
type MapPlaceManagementApplication interface {
	GetMapPlace(context.Context, *connect.Request[managev1.GetMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error)
	GetMapPlacesByIds(context.Context, *connect.Request[managev1.GetMapPlacesByIdsRequest]) (*connect.Response[managev1.GetMapPlacesByIdsResponse], error)
	ListMapPlacesAdmin(context.Context, *connect.Request[managev1.ListMapPlacesAdminRequest]) (*connect.Response[managev1.ListMapPlacesAdminResponse], error)
	CreateMapPlace(context.Context, *connect.Request[managev1.CreateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error)
	UpdateMapPlace(context.Context, *connect.Request[managev1.UpdateMapPlaceRequest]) (*connect.Response[managev1.MapPlace], error)
	DeleteMapPlace(context.Context, *connect.Request[managev1.DeleteMapPlaceRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

type MapPlaceManagementTools struct{ application MapPlaceManagementApplication }

func NewMapPlaceManagementTools(application MapPlaceManagementApplication) (*MapPlaceManagementTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Map Place management application is required")
	}
	return &MapPlaceManagementTools{application: application}, nil
}
func (*MapPlaceManagementTools) ToolNames() []string {
	return toolDefinitionNames(mapPlaceManagementTools)
}
func (*MapPlaceManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(mapPlaceManagementTools), nil
}

func (tools *MapPlaceManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolMapPlaceGet, ToolMapPlaceDelete:
		var input struct {
			ID string `json:"map_place_id"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("map_place_id", input.ID); err != nil {
			return executionError(err)
		}
		if name == ToolMapPlaceDelete {
			response, err := tools.application.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: input.ID}))
			if err != nil {
				return expectedToolError(err)
			}
			return contentResult(map[string]any{"success": response.Msg.GetSuccess()})
		}
		response, err := tools.application.GetMapPlace(ctx, connect.NewRequest(&managev1.GetMapPlaceRequest{Id: input.ID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"place": projectMapPlaceSettings(response.Msg)})
	case ToolMapPlaceGetMany:
		var input struct {
			IDs []string `json:"map_place_ids"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if len(input.IDs) < 1 || len(input.IDs) > 100 {
			return executionError(errors.New("map_place_ids must contain between 1 and 100 UUIDs"))
		}
		for _, id := range input.IDs {
			if err := validateUUID("map_place_ids", id); err != nil {
				return executionError(err)
			}
		}
		response, err := tools.application.GetMapPlacesByIds(ctx, connect.NewRequest(&managev1.GetMapPlacesByIdsRequest{Ids: input.IDs}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"items": projectMapPlaceSettingsMany(response.Msg.Places)})
	case ToolMapPlaceList:
		if err := rejectNullArguments(arguments, "query", "limit", "offset"); err != nil {
			return executionError(err)
		}
		var input struct {
			Query  string `json:"query"`
			Limit  *int32 `json:"limit"`
			Offset int32  `json:"offset"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		pagination, err := (programEventReferenceListArguments{Limit: input.Limit, Offset: input.Offset}).pagination()
		if err != nil {
			return executionError(err)
		}
		response, err := tools.application.ListMapPlacesAdmin(ctx, connect.NewRequest(&managev1.ListMapPlacesAdminRequest{Pagination: pagination, Filters: discoverySearchFilters(input.Query)}))
		if err != nil {
			return expectedToolError(err)
		}
		// The native Map Place list supplies total, limit, and offset but does not
		// populate HasMore. Derive continuation from that authoritative total.
		page := response.Msg.GetPagination()
		nextOffset := int64(page.GetOffset()) + int64(page.GetLimit())
		hasMore := nextOffset < int64(page.GetTotal())
		output := map[string]any{"items": projectMapPlaceSettingsMany(response.Msg.Places), "total": page.GetTotal(), "limit": page.GetLimit(), "offset": page.GetOffset(), "has_more": hasMore}
		if hasMore {
			output["next_offset"] = nextOffset
		}
		return contentResult(output)
	case ToolMapPlaceCreate, ToolMapPlaceSettingsUpdate:
		return tools.mutateSettings(ctx, name, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type mapPlaceSettingsArguments struct {
	ID                string                      `json:"map_place_id"`
	Name              *string                     `json:"name"`
	Address           *string                     `json:"address"`
	Lat               *float64                    `json:"lat"`
	Lng               *float64                    `json:"lng"`
	AddressComponents *managev1.AddressComponents `json:"address_components"`
	ImageFileID       *string                     `json:"image_file_id"`
	ClearImage        bool                        `json:"clear_image"`
	GooglePlaceID     *string                     `json:"google_place_id"`
}

func (tools *MapPlaceManagementTools) mutateSettings(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(arguments, "map_place_id", "name", "address", "lat", "lng", "address_components", "image_file_id", "clear_image", "google_place_id"); err != nil {
		return executionError(err)
	}
	if raw, ok := arguments["address_components"]; ok {
		var components mcpserver.ToolArguments
		if err := json.Unmarshal(raw, &components); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(components, "street", "city", "region", "country", "postal_code"); err != nil {
			return executionError(err)
		}
	}
	var input mapPlaceSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	for _, value := range []*string{input.Name, input.Address} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return executionError(errors.New("name and address must be nonempty when supplied"))
		}
	}
	if input.Lat != nil && (*input.Lat < -90 || *input.Lat > 90) {
		return executionError(errors.New("lat must be between -90 and 90"))
	}
	if input.Lng != nil && (*input.Lng < -180 || *input.Lng > 180) {
		return executionError(errors.New("lng must be between -180 and 180"))
	}
	if input.ImageFileID != nil {
		if err := validateUUID("image_file_id", *input.ImageFileID); err != nil {
			return executionError(err)
		}
	}
	var response *connect.Response[managev1.MapPlace]
	var err error
	if name == ToolMapPlaceCreate {
		if _, ok := arguments["map_place_id"]; ok {
			return executionError(errors.New("map_place_id is not accepted during create"))
		}
		if _, ok := arguments["clear_image"]; ok {
			return executionError(errors.New("clear_image is not accepted during create"))
		}
		if input.Name == nil || input.Address == nil || input.Lat == nil || input.Lng == nil {
			return executionError(errors.New("name, address, lat, and lng are required"))
		}
		response, err = tools.application.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{Name: *input.Name, Address: *input.Address, Lat: *input.Lat, Lng: *input.Lng, AddressComponents: input.AddressComponents, ImageFileId: input.ImageFileID, GooglePlaceId: input.GooglePlaceID}))
	} else {
		if err := validateUUID("map_place_id", input.ID); err != nil {
			return executionError(err)
		}
		response, err = tools.application.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{Id: input.ID, Name: input.Name, Address: input.Address, Lat: input.Lat, Lng: input.Lng, AddressComponents: input.AddressComponents, ImageFileId: input.ImageFileID, ClearImage: input.ClearImage, GooglePlaceId: input.GooglePlaceID}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"place": projectMapPlaceSettings(response.Msg)})
}

func projectMapPlaceSettings(place *managev1.MapPlace) map[string]any {
	output := map[string]any{"id": place.GetId(), "name": place.GetName(), "address": place.GetAddress(), "lat": place.GetLat(), "lng": place.GetLng()}
	if place.AddressComponents != nil {
		output["address_components"] = place.AddressComponents
	}
	if place.ImageFileId != nil {
		output["image_file_id"] = *place.ImageFileId
	}
	if place.GetImageAsset() != nil {
		output["image_url"] = place.GetImageAsset().GetUrl()
	}
	if place.GooglePlaceId != nil {
		output["google_place_id"] = *place.GooglePlaceId
	}
	return output
}
func projectMapPlaceSettingsMany(places []*managev1.MapPlace) []map[string]any {
	items := make([]map[string]any, 0, len(places))
	for _, place := range places {
		items = append(items, projectMapPlaceSettings(place))
	}
	return items
}
