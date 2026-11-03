package maptheme

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ConnectHandler serves the administrator Map Theme RPCs. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	themes *Service
}

// NewConnectHandler returns the administrator Connect adapter of themes.
func NewConnectHandler(themes *Service) *ConnectHandler {
	return &ConnectHandler{themes: themes}
}

// InternalConnectHandler serves the collaboration service's typed Map Theme
// snapshot boundary. Yjs is materialized only in the Collab process and is
// never stored.
type InternalConnectHandler struct {
	themes *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of themes.
func NewInternalConnectHandler(themes *Service) *InternalConnectHandler {
	return &InternalConnectHandler{themes: themes}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) ListMapThemes(ctx context.Context, _ *connect.Request[managev1.ListMapThemesRequest]) (*connect.Response[managev1.ListMapThemesResponse], error) {
	list, err := h.themes.ListMapThemes(ctx)
	themes := make([]*managev1.MapTheme, 0, len(list.Themes))
	for _, theme := range list.Themes {
		themes = append(themes, themeMessage(theme))
	}
	return respond(&managev1.ListMapThemesResponse{Themes: themes, DefaultMapThemeId: list.DefaultID}, err)
}

func (h *ConnectHandler) ResolveMapTheme(ctx context.Context, req *connect.Request[managev1.ResolveMapThemeRequest]) (*connect.Response[managev1.ResolvedMapTheme], error) {
	resolved, err := h.themes.ResolveMapTheme(ctx, ResolveMapThemeQuery{ThemeID: req.Msg.ThemeId, Scheme: req.Msg.Scheme})
	return respond(resolvedThemeMessage(resolved), err)
}

func (h *ConnectHandler) GetMapTheme(ctx context.Context, req *connect.Request[managev1.GetMapThemeRequest]) (*connect.Response[managev1.MapTheme], error) {
	theme, err := h.themes.GetMapTheme(ctx, GetMapThemeQuery{ID: req.Msg.Id})
	return respond(themeMessage(theme), err)
}

func (h *ConnectHandler) CreateMapTheme(ctx context.Context, req *connect.Request[managev1.CreateMapThemeRequest]) (*connect.Response[managev1.MapTheme], error) {
	theme, err := h.themes.CreateMapTheme(ctx, CreateMapThemeCommand{Snapshot: *manageSnapshotInput(req.Msg)})
	return respond(themeMessage(theme), err)
}

func (h *ConnectHandler) DeleteMapTheme(ctx context.Context, req *connect.Request[managev1.DeleteMapThemeRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.themes.DeleteMapTheme(ctx, DeleteMapThemeCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *ConnectHandler) CopyMapTheme(ctx context.Context, req *connect.Request[managev1.CopyMapThemeRequest]) (*connect.Response[managev1.MapTheme], error) {
	theme, err := h.themes.CopyMapTheme(ctx, CopyMapThemeCommand{ID: req.Msg.Id, Name: req.Msg.Name})
	return respond(themeMessage(theme), err)
}

func (h *ConnectHandler) SetDefaultMapTheme(ctx context.Context, req *connect.Request[managev1.SetDefaultMapThemeRequest]) (*connect.Response[managev1.SetDefaultMapThemeResponse], error) {
	id, err := h.themes.SetDefaultMapTheme(ctx, SetDefaultMapThemeCommand{ThemeID: req.Msg.ThemeId})
	return respond(&managev1.SetDefaultMapThemeResponse{DefaultMapThemeId: id}, err)
}

func (h *InternalConnectHandler) SaveMapThemeSnapshot(ctx context.Context, req *connect.Request[intrav1.SaveMapThemeSnapshotRequest]) (*connect.Response[intrav1.SaveMapThemeSnapshotResponse], error) {
	revision, err := h.themes.SaveCollaborationSnapshot(ctx, SaveCollaborationSnapshotCommand{
		ThemeID: req.Msg.ThemeId, Locale: req.Msg.Locale, ExpectedRevision: req.Msg.ExpectedRevision,
		Snapshot: internalSnapshotInput(req.Msg.Snapshot), ContributorMemberIDs: req.Msg.ContributorMemberIds,
	})
	return respond(&intrav1.SaveMapThemeSnapshotResponse{Success: true, Revision: revision, Locale: "und"}, err)
}

func (h *InternalConnectHandler) LoadMapThemeSnapshot(ctx context.Context, req *connect.Request[intrav1.LoadMapThemeSnapshotRequest]) (*connect.Response[intrav1.LoadMapThemeSnapshotResponse], error) {
	loaded, err := h.themes.LoadCollaborationSnapshot(ctx, LoadCollaborationSnapshotQuery{ThemeID: req.Msg.ThemeId, Locale: req.Msg.Locale})
	return respond(&intrav1.LoadMapThemeSnapshotResponse{Snapshot: documentSnapshotMessage(loaded.Snapshot), Revision: loaded.Revision, Locale: "und"}, err)
}

// manageSnapshotInput is the snapshot of an administrator create request.
func manageSnapshotInput(req *managev1.CreateMapThemeRequest) *SnapshotInput {
	if req == nil {
		return nil
	}
	input := &SnapshotInput{
		Name: req.Name, LightVariant: manageVariantInput(req.LightVariant), DarkVariant: manageVariantInput(req.DarkVariant),
	}
	if settings := req.Settings; settings != nil {
		input.Settings = &Settings{
			CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX,
			CalloutOffsetY: settings.CalloutOffsetY, CalloutFields: settings.CalloutFields,
			AttributionFontSize: settings.AttributionFontSize, ShowAreaLabels: settings.ShowAreaLabels,
			ShowPoiLabels: settings.ShowPoiLabels,
		}
	}
	return input
}

func manageVariantInput(input *managev1.MapThemeVariantInput) *Variant {
	if input == nil {
		return nil
	}
	return &Variant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor, LandColor: input.LandColor,
		RoadColor: input.RoadColor, BuildingFillColor: input.BuildingFillColor,
		BuildingStrokeEnabled: input.BuildingStrokeEnabled, BuildingStrokeColor: input.BuildingStrokeColor,
		CalloutLineColor: input.CalloutLineColor, CalloutTextColor: input.CalloutTextColor,
		CalloutBackgroundColor: input.CalloutBackgroundColor, CalloutDescriptionColor: input.CalloutDescriptionColor,
		AttributionColor: input.AttributionColor, LabelTextColor: input.LabelTextColor,
		ClusterColor: input.ClusterColor, ClusterHoverColor: input.ClusterHoverColor,
		ClusterTextColor: input.ClusterTextColor, ClusterTextHoverColor: input.ClusterTextHoverColor,
		CalloutHoverLineColor: input.CalloutHoverLineColor, CalloutHoverTextColor: input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func internalSnapshotInput(input *intrav1.MapThemeDocumentSnapshot) *SnapshotInput {
	if input == nil {
		return nil
	}
	snapshot := &SnapshotInput{
		Name: input.Name, LightVariant: internalVariantInput(input.LightVariant), DarkVariant: internalVariantInput(input.DarkVariant),
	}
	if settings := input.Settings; settings != nil {
		snapshot.Settings = &Settings{
			CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX,
			CalloutOffsetY: settings.CalloutOffsetY, CalloutFields: settings.CalloutFields,
			AttributionFontSize: settings.AttributionFontSize, ShowAreaLabels: settings.ShowAreaLabels,
			ShowPoiLabels: settings.ShowPoiLabels,
		}
	}
	return snapshot
}

func internalVariantInput(input *intrav1.MapThemeDocumentVariant) *Variant {
	if input == nil {
		return nil
	}
	return &Variant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor, LandColor: input.LandColor,
		RoadColor: input.RoadColor, BuildingFillColor: input.BuildingFillColor,
		BuildingStrokeEnabled: input.BuildingStrokeEnabled, BuildingStrokeColor: input.BuildingStrokeColor,
		CalloutLineColor: input.CalloutLineColor, CalloutTextColor: input.CalloutTextColor,
		CalloutBackgroundColor: input.CalloutBackgroundColor, CalloutDescriptionColor: input.CalloutDescriptionColor,
		AttributionColor: input.AttributionColor, LabelTextColor: input.LabelTextColor,
		ClusterColor: input.ClusterColor, ClusterHoverColor: input.ClusterHoverColor,
		ClusterTextColor: input.ClusterTextColor, ClusterTextHoverColor: input.ClusterTextHoverColor,
		CalloutHoverLineColor: input.CalloutHoverLineColor, CalloutHoverTextColor: input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func variantMessage(themeID, scheme string, v Variant) *managev1.MapThemeVariant {
	return &managev1.MapThemeVariant{
		// The transport keeps a stable conceptual variant id for clients, but
		// variants are not independently persisted rows.
		Id: themeID + ":" + scheme, BackgroundColor: v.BackgroundColor, WaterColor: v.WaterColor,
		LandColor: v.LandColor, RoadColor: v.RoadColor, BuildingFillColor: v.BuildingFillColor,
		BuildingStrokeEnabled: v.BuildingStrokeEnabled, BuildingStrokeColor: v.BuildingStrokeColor,
		CalloutLineColor: v.CalloutLineColor, CalloutTextColor: v.CalloutTextColor,
		CalloutBackgroundColor: v.CalloutBackgroundColor, CalloutDescriptionColor: v.CalloutDescriptionColor,
		AttributionColor: v.AttributionColor, LabelTextColor: v.LabelTextColor,
		ClusterColor: v.ClusterColor, ClusterHoverColor: v.ClusterHoverColor,
		ClusterTextColor: v.ClusterTextColor, ClusterTextHoverColor: v.ClusterTextHoverColor,
		CalloutHoverLineColor: v.CalloutHoverLineColor, CalloutHoverTextColor: v.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: v.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  v.CalloutHoverBackgroundColor,
	}
}

func settingsMessage(settings Settings) *managev1.MapThemeSettings {
	return &managev1.MapThemeSettings{
		CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX,
		CalloutOffsetY: settings.CalloutOffsetY, CalloutFields: settings.CalloutFields,
		AttributionFontSize: settings.AttributionFontSize, ShowAreaLabels: settings.ShowAreaLabels,
		ShowPoiLabels: settings.ShowPoiLabels,
	}
}

func themeMessage(theme Theme) *managev1.MapTheme {
	return &managev1.MapTheme{
		Id: theme.ID, Name: theme.Snapshot.Name, Revision: theme.Revision,
		CreatedAt: timestamppb.New(theme.CreatedAt), UpdatedAt: timestamppb.New(theme.UpdatedAt),
		Settings:     settingsMessage(theme.Snapshot.Settings),
		LightVariant: variantMessage(theme.ID, "light", theme.Snapshot.LightVariant),
		DarkVariant:  variantMessage(theme.ID, "dark", theme.Snapshot.DarkVariant),
	}
}

func resolvedThemeMessage(resolved ResolvedTheme) *managev1.ResolvedMapTheme {
	return &managev1.ResolvedMapTheme{
		ThemeId: resolved.ThemeID, Scheme: resolved.Scheme, Settings: settingsMessage(resolved.Settings),
		Variant: variantMessage(resolved.ThemeID, resolved.Scheme, resolved.Variant),
	}
}

func documentVariantMessage(input Variant) *intrav1.MapThemeDocumentVariant {
	return &intrav1.MapThemeDocumentVariant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor, LandColor: input.LandColor,
		RoadColor: input.RoadColor, BuildingFillColor: input.BuildingFillColor,
		BuildingStrokeEnabled: input.BuildingStrokeEnabled, BuildingStrokeColor: input.BuildingStrokeColor,
		CalloutLineColor: input.CalloutLineColor, CalloutTextColor: input.CalloutTextColor,
		CalloutBackgroundColor: input.CalloutBackgroundColor, CalloutDescriptionColor: input.CalloutDescriptionColor,
		AttributionColor: input.AttributionColor, LabelTextColor: input.LabelTextColor,
		ClusterColor: input.ClusterColor, ClusterHoverColor: input.ClusterHoverColor,
		ClusterTextColor: input.ClusterTextColor, ClusterTextHoverColor: input.ClusterTextHoverColor,
		CalloutHoverLineColor: input.CalloutHoverLineColor, CalloutHoverTextColor: input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func documentSnapshotMessage(snapshot Snapshot) *intrav1.MapThemeDocumentSnapshot {
	settings := snapshot.Settings
	return &intrav1.MapThemeDocumentSnapshot{
		Name: snapshot.Name,
		Settings: &intrav1.MapThemeDocumentSettings{
			CalloutScale: settings.CalloutScale, CalloutOffsetX: settings.CalloutOffsetX,
			CalloutOffsetY: settings.CalloutOffsetY, CalloutFields: settings.CalloutFields,
			AttributionFontSize: settings.AttributionFontSize, ShowAreaLabels: settings.ShowAreaLabels,
			ShowPoiLabels: settings.ShowPoiLabels,
		},
		LightVariant: documentVariantMessage(snapshot.LightVariant),
		DarkVariant:  documentVariantMessage(snapshot.DarkVariant),
	}
}
