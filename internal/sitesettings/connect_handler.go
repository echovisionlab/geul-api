package sitesettings

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

// ConnectHandler serves the Site Settings management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedSiteSettingServiceHandler
	settings *Service
}

// NewConnectHandler returns the Connect adapter of settings.
func NewConnectHandler(settings *Service) *ConnectHandler {
	return &ConnectHandler{settings: settings}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// requestedValue is the plain value of a requested setting; an absent value
// is null.
func requestedValue(value *structpb.Value) structured.Value {
	if value == nil {
		return nil
	}
	return value.AsInterface()
}

// requestedSettings converts requested setting messages into command values.
func requestedSettings(settings []*managev1.SiteSetting) []Setting {
	requested := make([]Setting, 0, len(settings))
	for _, setting := range settings {
		requested = append(requested, Setting{Key: setting.Key, Value: requestedValue(setting.Value)})
	}
	return requested
}

func (h *ConnectHandler) GetSettings(ctx context.Context, _ *connect.Request[managev1.GetSettingsRequest]) (*connect.Response[managev1.GetSettingsResponse], error) {
	settings, err := h.settings.GetSettings(ctx)
	return respond(&managev1.GetSettingsResponse{Settings: settings}, err)
}

func (h *ConnectHandler) GetSetting(ctx context.Context, req *connect.Request[managev1.GetSettingRequest]) (*connect.Response[managev1.GetSettingResponse], error) {
	setting, err := h.settings.GetSetting(ctx, GetSettingQuery{Key: req.Msg.Key})
	return respond(&managev1.GetSettingResponse{Setting: setting}, err)
}

func (h *ConnectHandler) SetSetting(ctx context.Context, req *connect.Request[managev1.SetSettingRequest]) (*connect.Response[managev1.SetSettingResponse], error) {
	update, err := h.settings.SetSetting(ctx, SetSettingCommand{Key: req.Msg.Key, Value: requestedValue(req.Msg.Value)})
	return respond(&managev1.SetSettingResponse{Success: true, OgGenerationRunId: update.OGGenerationRunID}, err)
}

func (h *ConnectHandler) SetManySettings(ctx context.Context, req *connect.Request[managev1.SetManySettingsRequest]) (*connect.Response[managev1.SetManySettingsResponse], error) {
	update, err := h.settings.SetManySettings(ctx, SetManySettingsCommand{Settings: requestedSettings(req.Msg.Settings)})
	return respond(&managev1.SetManySettingsResponse{Success: true, OgGenerationRunId: update.OGGenerationRunID}, err)
}

func (h *ConnectHandler) AddSiteLoaderAsset(ctx context.Context, req *connect.Request[managev1.AddSiteLoaderAssetRequest]) (*connect.Response[managev1.AddSiteLoaderAssetResponse], error) {
	err := h.settings.AddSiteLoaderAsset(ctx, AddSiteLoaderAssetCommand{FileID: req.Msg.FileId})
	return respond(&managev1.AddSiteLoaderAssetResponse{Success: true}, err)
}

func (h *ConnectHandler) RemoveSiteLoaderAsset(ctx context.Context, req *connect.Request[managev1.RemoveSiteLoaderAssetRequest]) (*connect.Response[managev1.RemoveSiteLoaderAssetResponse], error) {
	err := h.settings.RemoveSiteLoaderAsset(ctx, RemoveSiteLoaderAssetCommand{FileID: req.Msg.FileId})
	return respond(&managev1.RemoveSiteLoaderAssetResponse{Success: true}, err)
}
