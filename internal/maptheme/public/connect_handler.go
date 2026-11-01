package public

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Map Theme RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedMapThemeServiceHandler
	themes *Service
}

// NewConnectHandler returns the Connect adapter of themes.
func NewConnectHandler(themes *Service) *ConnectHandler {
	return &ConnectHandler{themes: themes}
}

func (h *ConnectHandler) ResolveByIds(ctx context.Context, req *connect.Request[openv1.ResolveMapThemesByIdsRequest]) (*connect.Response[openv1.ResolveMapThemesByIdsResponse], error) {
	resolutions, err := h.themes.ResolveByIDs(ctx, ResolveByIDsQuery{ThemeIDs: req.Msg.RequestedThemeIds})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	results := make([]*openv1.ResolveMapThemeByIdResult, 0, len(resolutions))
	for _, resolution := range resolutions {
		results = append(results, &openv1.ResolveMapThemeByIdResult{RequestedThemeId: resolution.RequestedID, Theme: themeMessage(resolution.Theme)})
	}
	return connect.NewResponse(&openv1.ResolveMapThemesByIdsResponse{Results: results}), nil
}

func (h *ConnectHandler) Resolve(ctx context.Context, req *connect.Request[openv1.ResolveMapThemeRequest]) (*connect.Response[openv1.ResolvedMapTheme], error) {
	resolved, err := h.themes.Resolve(ctx, ResolveQuery{ThemeID: req.Msg.ThemeId, Scheme: req.Msg.Scheme})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.ResolvedMapTheme{
		ThemeId: resolved.ThemeID, Scheme: resolved.Scheme, Settings: settingsMessage(resolved.Settings),
		Variant: variantMessage(resolved.ThemeID, resolved.Scheme, resolved.Variant),
	}), nil
}

func variantMessage(themeID, scheme string, variant maptheme.Variant) *openv1.MapThemeVariant {
	return &openv1.MapThemeVariant{
		Id: themeID + ":" + scheme, BackgroundColor: variant.BackgroundColor, WaterColor: variant.WaterColor,
		LandColor: variant.LandColor, RoadColor: variant.RoadColor,
		BuildingFillColor: variant.BuildingFillColor, BuildingStrokeEnabled: variant.BuildingStrokeEnabled,
		BuildingStrokeColor: variant.BuildingStrokeColor, CalloutLineColor: variant.CalloutLineColor,
		CalloutTextColor: variant.CalloutTextColor, CalloutBackgroundColor: variant.CalloutBackgroundColor,
		CalloutDescriptionColor: variant.CalloutDescriptionColor, AttributionColor: variant.AttributionColor,
		LabelTextColor: variant.LabelTextColor, ClusterColor: variant.ClusterColor,
		ClusterHoverColor: variant.ClusterHoverColor, ClusterTextColor: variant.ClusterTextColor,
		ClusterTextHoverColor:        variant.ClusterTextHoverColor,
		CalloutHoverLineColor:        variant.CalloutHoverLineColor,
		CalloutHoverTextColor:        variant.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: variant.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  variant.CalloutHoverBackgroundColor,
	}
}

func settingsMessage(settings maptheme.Settings) *openv1.MapThemeSettings {
	return &openv1.MapThemeSettings{
		CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX,
		CalloutOffsetY: settings.CalloutOffsetY, CalloutFields: settings.CalloutFields,
		AttributionFontSize: settings.AttributionFontSize, ShowAreaLabels: settings.ShowAreaLabels,
		ShowPoiLabels: settings.ShowPoiLabels,
	}
}

func themeMessage(theme maptheme.Theme) *openv1.MapTheme {
	return &openv1.MapTheme{
		Id: theme.ID, Name: theme.Snapshot.Name, CreatedAt: timestamppb.New(theme.CreatedAt),
		Settings:     settingsMessage(theme.Snapshot.Settings),
		LightVariant: variantMessage(theme.ID, "light", theme.Snapshot.LightVariant),
		DarkVariant:  variantMessage(theme.ID, "dark", theme.Snapshot.DarkVariant),
	}
}
