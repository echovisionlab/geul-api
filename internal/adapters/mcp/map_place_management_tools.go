package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
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
	GetMapPlace(context.Context, referencecatalog.GetMapPlaceQuery) (referencecatalog.MapPlaceView, error)
	GetMapPlacesByIDs(context.Context, referencecatalog.GetMapPlacesByIDsQuery) ([]referencecatalog.MapPlaceView, error)
	ListMapPlacesAdmin(context.Context, referencecatalog.ListMapPlacesAdminQuery) (referencecatalog.Page[referencecatalog.MapPlaceView], error)
	CreateMapPlace(context.Context, referencecatalog.CreateMapPlaceCommand) (referencecatalog.MapPlaceView, error)
	UpdateMapPlace(context.Context, referencecatalog.UpdateMapPlaceCommand) (referencecatalog.MapPlaceView, error)
	DeleteMapPlace(context.Context, referencecatalog.DeleteMapPlaceCommand) error
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
			if err := tools.application.DeleteMapPlace(ctx, referencecatalog.DeleteMapPlaceCommand{ID: input.ID}); err != nil {
				return expectedToolError(err)
			}
			return contentResult(map[string]any{"success": true})
		}
		place, err := tools.application.GetMapPlace(ctx, referencecatalog.GetMapPlaceQuery{ID: input.ID})
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"place": projectMapPlaceSettings(place)})
	case ToolMapPlaceGetMany:
		return tools.getPlaces(ctx, arguments)
	case ToolMapPlaceList:
		return tools.listPlaces(ctx, arguments)
	case ToolMapPlaceCreate, ToolMapPlaceSettingsUpdate:
		return tools.mutateSettings(ctx, name, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

func (tools *MapPlaceManagementTools) getPlaces(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
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
	places, err := tools.application.GetMapPlacesByIDs(ctx, referencecatalog.GetMapPlacesByIDsQuery{IDs: input.IDs})
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"items": projectMapPlaceSettingsMany(places)})
}

func (tools *MapPlaceManagementTools) listPlaces(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
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
	listed, err := tools.application.ListMapPlacesAdmin(ctx, referencecatalog.ListMapPlacesAdminQuery{
		Filters: discoverySearchFilters(input.Query), Limit: pagination.GetLimit(), Offset: pagination.GetOffset(),
	})
	if err != nil {
		return expectedToolError(err)
	}
	// The native Map Place list supplies total, limit, and offset but does not
	// populate HasMore. Derive continuation from that authoritative total.
	page := listed.Pagination
	nextOffset := int64(page.Offset) + int64(page.Limit)
	hasMore := nextOffset < int64(page.Total)
	output := map[string]any{"items": projectMapPlaceSettingsMany(listed.Items), "total": page.Total, "limit": page.Limit, "offset": page.Offset, "has_more": hasMore}
	if hasMore {
		output["next_offset"] = nextOffset
	}
	return contentResult(output)
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
	input, err := decodeMapPlaceSettings(arguments)
	if err != nil {
		return executionError(err)
	}
	var place referencecatalog.MapPlaceView
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
		place, err = tools.application.CreateMapPlace(ctx, referencecatalog.CreateMapPlaceCommand{
			Name: *input.Name, Address: *input.Address, Lat: *input.Lat, Lng: *input.Lng,
			AddressComponents: referencecatalog.AddressComponentsOf(input.AddressComponents), ImageFileID: input.ImageFileID, GooglePlaceID: input.GooglePlaceID,
		})
	} else {
		if err := validateUUID("map_place_id", input.ID); err != nil {
			return executionError(err)
		}
		place, err = tools.application.UpdateMapPlace(ctx, referencecatalog.UpdateMapPlaceCommand{
			ID: input.ID, Name: input.Name, Address: input.Address, Lat: input.Lat, Lng: input.Lng,
			AddressComponents: referencecatalog.AddressComponentsOf(input.AddressComponents), ImageFileID: input.ImageFileID,
			ClearImage: input.ClearImage, GooglePlaceID: input.GooglePlaceID,
		})
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"place": projectMapPlaceSettings(place)})
}

// decodeMapPlaceSettings reads and checks the supplied place settings.
func decodeMapPlaceSettings(arguments mcpserver.ToolArguments) (mapPlaceSettingsArguments, error) {
	var input mapPlaceSettingsArguments
	if err := rejectNullArguments(arguments, "map_place_id", "name", "address", "lat", "lng", "address_components", "image_file_id", "clear_image", "google_place_id"); err != nil {
		return input, err
	}
	if raw, ok := arguments["address_components"]; ok {
		var components mcpserver.ToolArguments
		if err := json.Unmarshal(raw, &components); err != nil {
			return input, err
		}
		if err := rejectNullArguments(components, "street", "city", "region", "country", "postal_code"); err != nil {
			return input, err
		}
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return input, err
	}
	for _, value := range []*string{input.Name, input.Address} {
		if value != nil && strings.TrimSpace(*value) == "" {
			return input, errors.New("name and address must be nonempty when supplied")
		}
	}
	if input.Lat != nil && (*input.Lat < -90 || *input.Lat > 90) {
		return input, errors.New("lat must be between -90 and 90")
	}
	if input.Lng != nil && (*input.Lng < -180 || *input.Lng > 180) {
		return input, errors.New("lng must be between -180 and 180")
	}
	if input.ImageFileID != nil {
		if err := validateUUID("image_file_id", *input.ImageFileID); err != nil {
			return input, err
		}
	}
	return input, nil
}

// projectMapPlaceSettings projects the manage API's rendering of a place.
func projectMapPlaceSettings(view referencecatalog.MapPlaceView) map[string]any {
	place := referencecatalog.MapPlaceMessage(view)
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
func projectMapPlaceSettingsMany(places []referencecatalog.MapPlaceView) []map[string]any {
	items := make([]map[string]any, 0, len(places))
	for _, place := range places {
		items = append(items, projectMapPlaceSettings(place))
	}
	return items
}
