package public

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

type MapThemeService struct {
	openv1connect.UnimplementedMapThemeServiceHandler
	db *gorm.DB
}

func NewMapThemeService(db *gorm.DB) *MapThemeService { return &MapThemeService{db: db} }

func publicMapThemeVariantProto(themeID, scheme string, variant *model.MapThemeVariant) *openv1.MapThemeVariant {
	proto := &openv1.MapThemeVariant{
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
	return proto
}

func publicMapThemeProto(theme *model.MapTheme) (*openv1.MapTheme, error) {
	proto := &openv1.MapTheme{
		Id: theme.ID, Name: theme.Name, CreatedAt: timestamppb.New(theme.CreatedAt),
		Settings: &openv1.MapThemeSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		},
		LightVariant: publicMapThemeVariantProto(theme.ID, "light", &theme.LightVariant),
		DarkVariant:  publicMapThemeVariantProto(theme.ID, "dark", &theme.DarkVariant),
	}
	return proto, nil
}

func publicDefaultMapThemeID(ctx context.Context, db *gorm.DB) (string, error) {
	var settings model.SiteSettings
	if err := db.WithContext(ctx).Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error; err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.DefaultMapThemeID) == "" {
		return "", fmt.Errorf("site settings default map theme is empty")
	}
	return settings.DefaultMapThemeID, nil
}

func publicLoadMapThemes(
	ctx context.Context, db *gorm.DB, requestedIDs []string,
) (string, map[string]*model.MapTheme, error) {
	var defaultID string
	var byID map[string]*model.MapTheme
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		defaultID, err = publicDefaultMapThemeID(ctx, tx)
		if err != nil {
			return err
		}
		unique := map[string]struct{}{defaultID: {}}
		for _, id := range requestedIDs {
			if id != "" {
				unique[id] = struct{}{}
			}
		}
		ids := make([]string, 0, len(unique))
		for id := range unique {
			ids = append(ids, id)
		}
		var themes []model.MapTheme
		if err := tx.Where("id IN ?", ids).Find(&themes).Error; err != nil {
			return err
		}
		byID = make(map[string]*model.MapTheme, len(themes))
		for i := range themes {
			byID[themes[i].ID] = &themes[i]
		}
		if byID[defaultID] == nil {
			return fmt.Errorf("default map theme %s is missing", defaultID)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return defaultID, byID, err
}

func (s *MapThemeService) ResolveByIds(
	ctx context.Context, req *connect.Request[openv1.ResolveMapThemesByIdsRequest],
) (*connect.Response[openv1.ResolveMapThemesByIdsResponse], error) {
	requested := make([]string, 0, len(req.Msg.RequestedThemeIds))
	for _, id := range req.Msg.RequestedThemeIds {
		if id == "" {
			continue
		}
		if _, err := uuidutil.ParseCanonical(id, "requested_theme_ids"); err != nil {
			return nil, errs.InvalidArgument("requested_theme_ids", "must contain only canonical UUIDs")
		}
		requested = append(requested, id)
	}
	if len(requested) == 0 {
		return connect.NewResponse(&openv1.ResolveMapThemesByIdsResponse{Results: []*openv1.ResolveMapThemeByIdResult{}}), nil
	}
	defaultID, themes, err := publicLoadMapThemes(ctx, s.db, requested)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("resolve map themes: %w", err))
	}
	defaultTheme := themes[defaultID]
	results := make([]*openv1.ResolveMapThemeByIdResult, 0, len(requested))
	for _, requestedID := range requested {
		resolved := themes[requestedID]
		if resolved == nil {
			resolved = defaultTheme
		}
		proto, err := publicMapThemeProto(resolved)
		if err != nil {
			return nil, errs.Internal(err)
		}
		results = append(results, &openv1.ResolveMapThemeByIdResult{RequestedThemeId: requestedID, Theme: proto})
	}
	return connect.NewResponse(&openv1.ResolveMapThemesByIdsResponse{Results: results}), nil
}

func (s *MapThemeService) Resolve(
	ctx context.Context, req *connect.Request[openv1.ResolveMapThemeRequest],
) (*connect.Response[openv1.ResolvedMapTheme], error) {
	scheme := req.Msg.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return nil, errs.InvalidArgument("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if req.Msg.ThemeId != nil {
		requestedID = *req.Msg.ThemeId
		if requestedID != "" {
			if _, err := uuidutil.ParseCanonical(requestedID, "theme_id"); err != nil {
				return nil, errs.InvalidArgument("theme_id", "must be a canonical UUID")
			}
		}
	}
	defaultID, themes, err := publicLoadMapThemes(ctx, s.db, []string{requestedID})
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("resolve map theme: %w", err))
	}
	theme := themes[requestedID]
	if theme == nil {
		theme = themes[defaultID]
	}
	variant := &theme.LightVariant
	if scheme == "dark" {
		variant = &theme.DarkVariant
	}
	return connect.NewResponse(&openv1.ResolvedMapTheme{
		ThemeId: theme.ID, Scheme: scheme,
		Settings: &openv1.MapThemeSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		}, Variant: publicMapThemeVariantProto(theme.ID, scheme, variant),
	}), nil
}
