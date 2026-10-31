package public

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// Deps are the collaborators of the public Map Theme reads.
type Deps struct {
	DB *gorm.DB
}

// Service holds the public Map Theme reads.
type Service struct {
	db *gorm.DB
}

// NewService validates deps and returns the public Map Theme reads.
func NewService(deps Deps) (*Service, error) {
	if deps.DB == nil {
		return nil, errors.New("public map theme dependencies are required: DB")
	}
	return &Service{db: deps.DB}, nil
}

// ResolveByIDsQuery names themes to resolve; an empty ID is skipped.
type ResolveByIDsQuery struct {
	ThemeIDs []string
}

// ResolveQuery names a theme to resolve, falling back to the site default,
// and the color scheme to resolve it in ("light" when empty).
type ResolveQuery struct {
	ThemeID *string
	Scheme  string
}

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

func publicMapThemeProto(theme *model.MapTheme) *openv1.MapTheme {
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
	return proto
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
	err := mutation.Read(ctx, db, func(tx *gorm.DB) error {
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
	})
	return defaultID, byID, err
}

// ResolveByIDs resolves each requested theme, falling back to the site
// default for a missing one.
func (s *Service) ResolveByIDs(ctx context.Context, query ResolveByIDsQuery) ([]*openv1.ResolveMapThemeByIdResult, error) {
	requested := make([]string, 0, len(query.ThemeIDs))
	for _, id := range query.ThemeIDs {
		if id == "" {
			continue
		}
		if _, err := uuidutil.ParseCanonical(id, "requested_theme_ids"); err != nil {
			return nil, domainerr.InvalidField("requested_theme_ids", "must contain only canonical UUIDs")
		}
		requested = append(requested, id)
	}
	if len(requested) == 0 {
		return []*openv1.ResolveMapThemeByIdResult{}, nil
	}
	defaultID, themes, err := publicLoadMapThemes(ctx, s.db, requested)
	if err != nil {
		return nil, fmt.Errorf("resolve map themes: %w", err)
	}
	defaultTheme := themes[defaultID]
	results := make([]*openv1.ResolveMapThemeByIdResult, 0, len(requested))
	for _, requestedID := range requested {
		resolved := themes[requestedID]
		if resolved == nil {
			resolved = defaultTheme
		}
		proto := publicMapThemeProto(resolved)
		results = append(results, &openv1.ResolveMapThemeByIdResult{RequestedThemeId: requestedID, Theme: proto})
	}
	return results, nil
}

func (s *Service) Resolve(ctx context.Context, query ResolveQuery) (*openv1.ResolvedMapTheme, error) {
	scheme := query.Scheme
	if scheme == "" {
		scheme = "light"
	}
	if scheme != "light" && scheme != "dark" {
		return nil, domainerr.InvalidField("scheme", "must be 'light' or 'dark'")
	}
	requestedID := ""
	if query.ThemeID != nil {
		requestedID = *query.ThemeID
		if requestedID != "" {
			if _, err := uuidutil.ParseCanonical(requestedID, "theme_id"); err != nil {
				return nil, domainerr.InvalidField("theme_id", "must be a canonical UUID")
			}
		}
	}
	defaultID, themes, err := publicLoadMapThemes(ctx, s.db, []string{requestedID})
	if err != nil {
		return nil, fmt.Errorf("resolve map theme: %w", err)
	}
	theme := themes[requestedID]
	if theme == nil {
		theme = themes[defaultID]
	}
	variant := &theme.LightVariant
	if scheme == "dark" {
		variant = &theme.DarkVariant
	}
	return &openv1.ResolvedMapTheme{
		ThemeId: theme.ID, Scheme: scheme,
		Settings: &openv1.MapThemeSettings{
			CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
			CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: theme.CalloutFields,
			AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
			ShowPoiLabels: theme.ShowPoiLabels,
		}, Variant: publicMapThemeVariantProto(theme.ID, scheme, variant),
	}, nil
}
