package maptheme

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

var (
	mapThemeHexColorPattern  = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	mapThemeRGBColorPattern  = regexp.MustCompile(`(?i)^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$`)
	mapThemeRGBAColorPattern = regexp.MustCompile(
		`(?i)^rgba\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*((?:0(?:\.\d+)?)|(?:1(?:\.0+)?))\s*\)$`,
	)
	mapThemeCalloutFields = map[string]struct{}{
		"name": {}, "address": {}, "coordinates": {}, "street": {},
		"city": {}, "region": {}, "country": {}, "postalCode": {},
	}
)

// Settings are the callout and label settings of a Map Theme.
type Settings struct {
	CalloutScale        float64  `json:"callout_scale"`
	CalloutOffsetX      int32    `json:"callout_offset_x"`
	CalloutOffsetY      int32    `json:"callout_offset_y"`
	CalloutFields       []string `json:"callout_fields"`
	AttributionFontSize int32    `json:"attribution_font_size"`
	ShowAreaLabels      bool     `json:"show_area_labels"`
	ShowPoiLabels       bool     `json:"show_poi_labels"`
}

// Variant is the color set of one Map Theme color scheme.
type Variant struct {
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

// Snapshot is the complete editable state of a Map Theme.
type Snapshot struct {
	Name         string   `json:"name"`
	Settings     Settings `json:"settings"`
	LightVariant Variant  `json:"light_variant"`
	DarkVariant  Variant  `json:"dark_variant"`
}

// SnapshotInput is a Snapshot as a caller submits it; a nil part is missing.
type SnapshotInput struct {
	Name         string
	Settings     *Settings
	LightVariant *Variant
	DarkVariant  *Variant
}

// snapshot validates and normalizes input. prefix qualifies the field of a
// missing part, as a collaboration save nests the snapshot.
func (input *SnapshotInput) snapshot(prefix string) (*Snapshot, error) {
	if input == nil {
		return nil, domainerr.Required("snapshot")
	}
	if input.Settings == nil {
		return nil, domainerr.Required(prefix + "settings")
	}
	if input.LightVariant == nil {
		return nil, domainerr.Required(prefix + "light_variant")
	}
	if input.DarkVariant == nil {
		return nil, domainerr.Required(prefix + "dark_variant")
	}
	snapshot := &Snapshot{
		Name: input.Name, Settings: *input.Settings,
		LightVariant: *input.LightVariant, DarkVariant: *input.DarkVariant,
	}
	snapshot.Settings.CalloutFields = append([]string(nil), input.Settings.CalloutFields...)
	return snapshot, validateMapThemeSnapshot(snapshot)
}

func normalizeMapThemeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domainerr.Required("name")
	}
	if utf8.RuneCountInString(value) > 255 {
		return "", domainerr.InvalidField("name", "must be at most 255 characters")
	}
	return value, nil
}

func normalizeMapThemeID(value, field string) (string, error) {
	if value == "" {
		return "", domainerr.Required(field)
	}
	if _, err := uuidutil.ParseCanonical(value, field); err != nil {
		return "", domainerr.InvalidField(field, "must be a canonical UUID")
	}
	return value, nil
}

func validateMapThemeSettings(settings Settings) error {
	if math.IsNaN(settings.CalloutScale) || math.IsInf(settings.CalloutScale, 0) ||
		settings.CalloutScale < 0.5 || settings.CalloutScale > 2 {
		return domainerr.InvalidField("settings.callout_scale", "must be between 0.5 and 2")
	}
	if settings.CalloutOffsetX < -50 || settings.CalloutOffsetX > 50 {
		return domainerr.InvalidField("settings.callout_offset_x", "must be an integer between -50 and 50")
	}
	if settings.CalloutOffsetY < -50 || settings.CalloutOffsetY > 50 {
		return domainerr.InvalidField("settings.callout_offset_y", "must be an integer between -50 and 50")
	}
	if len(settings.CalloutFields) < 1 || len(settings.CalloutFields) > 8 {
		return domainerr.InvalidField("settings.callout_fields", "must contain between 1 and 8 fields")
	}
	for _, field := range settings.CalloutFields {
		if _, ok := mapThemeCalloutFields[field]; !ok {
			return domainerr.InvalidField("settings.callout_fields", fmt.Sprintf("unsupported field %q", field))
		}
	}
	if settings.AttributionFontSize < 9 || settings.AttributionFontSize > 14 {
		return domainerr.InvalidField("settings.attribution_font_size", "must be an integer between 9 and 14")
	}
	return nil
}

func normalizeMapThemeColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > 50 {
		return "", false
	}
	if value == "transparent" || mapThemeHexColorPattern.MatchString(value) {
		return value, true
	}
	if matches := mapThemeRGBColorPattern.FindStringSubmatch(value); matches != nil {
		for _, channel := range matches[1:] {
			parsed, _ := strconv.Atoi(channel)
			if parsed > 255 {
				return "", false
			}
		}
		return value, true
	}
	if matches := mapThemeRGBAColorPattern.FindStringSubmatch(value); matches != nil {
		for _, channel := range matches[1:4] {
			parsed, _ := strconv.Atoi(channel)
			if parsed > 255 {
				return "", false
			}
		}
		return value, true
	}
	return "", false
}

func validateMapThemeVariant(field string, variant *Variant) error {
	if variant == nil {
		return domainerr.Required(field)
	}
	colors := []struct {
		name  string
		value *string
	}{
		{"background_color", &variant.BackgroundColor},
		{"water_color", &variant.WaterColor},
		{"land_color", &variant.LandColor},
		{"road_color", &variant.RoadColor},
		{"building_fill_color", &variant.BuildingFillColor},
		{"building_stroke_color", &variant.BuildingStrokeColor},
		{"callout_line_color", &variant.CalloutLineColor},
		{"callout_text_color", &variant.CalloutTextColor},
		{"callout_background_color", &variant.CalloutBackgroundColor},
		{"callout_description_color", &variant.CalloutDescriptionColor},
		{"attribution_color", &variant.AttributionColor},
		{"label_text_color", &variant.LabelTextColor},
		{"cluster_color", &variant.ClusterColor},
		{"cluster_hover_color", &variant.ClusterHoverColor},
		{"cluster_text_color", &variant.ClusterTextColor},
		{"cluster_text_hover_color", &variant.ClusterTextHoverColor},
		{"callout_hover_line_color", &variant.CalloutHoverLineColor},
		{"callout_hover_text_color", &variant.CalloutHoverTextColor},
		{"callout_hover_description_color", &variant.CalloutHoverDescriptionColor},
		{"callout_hover_background_color", &variant.CalloutHoverBackgroundColor},
	}
	for _, color := range colors {
		normalized, ok := normalizeMapThemeColor(*color.value)
		if !ok {
			return domainerr.InvalidField(field+"."+color.name, "must be hex, rgb(), rgba(), or transparent")
		}
		*color.value = normalized
	}
	return nil
}

func validateMapThemeSnapshot(snapshot *Snapshot) error {
	if snapshot == nil {
		return domainerr.Required("snapshot")
	}
	name, err := normalizeMapThemeName(snapshot.Name)
	if err != nil {
		return err
	}
	snapshot.Name = name
	if err := validateMapThemeSettings(snapshot.Settings); err != nil {
		return err
	}
	if err := validateMapThemeVariant("light_variant", &snapshot.LightVariant); err != nil {
		return err
	}
	return validateMapThemeVariant("dark_variant", &snapshot.DarkVariant)
}

func mapThemeVariantModel(input Variant) model.MapThemeVariant {
	return model.MapThemeVariant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor,
		LandColor: input.LandColor, RoadColor: input.RoadColor,
		BuildingFillColor: input.BuildingFillColor, BuildingStrokeEnabled: input.BuildingStrokeEnabled,
		BuildingStrokeColor: input.BuildingStrokeColor, CalloutLineColor: input.CalloutLineColor,
		CalloutTextColor: input.CalloutTextColor, CalloutBackgroundColor: input.CalloutBackgroundColor,
		CalloutDescriptionColor: input.CalloutDescriptionColor, AttributionColor: input.AttributionColor,
		LabelTextColor: input.LabelTextColor, ClusterColor: input.ClusterColor,
		ClusterHoverColor: input.ClusterHoverColor, ClusterTextColor: input.ClusterTextColor,
		ClusterTextHoverColor: input.ClusterTextHoverColor, CalloutHoverLineColor: input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func variantFromModel(input model.MapThemeVariant) Variant {
	return Variant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor,
		LandColor: input.LandColor, RoadColor: input.RoadColor,
		BuildingFillColor: input.BuildingFillColor, BuildingStrokeEnabled: input.BuildingStrokeEnabled,
		BuildingStrokeColor: input.BuildingStrokeColor, CalloutLineColor: input.CalloutLineColor,
		CalloutTextColor: input.CalloutTextColor, CalloutBackgroundColor: input.CalloutBackgroundColor,
		CalloutDescriptionColor: input.CalloutDescriptionColor, AttributionColor: input.AttributionColor,
		LabelTextColor: input.LabelTextColor, ClusterColor: input.ClusterColor,
		ClusterHoverColor: input.ClusterHoverColor, ClusterTextColor: input.ClusterTextColor,
		ClusterTextHoverColor: input.ClusterTextHoverColor, CalloutHoverLineColor: input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func settingsFromModel(theme *model.MapTheme) Settings {
	return Settings{
		CalloutScale: float64(theme.CalloutScale), CalloutOffsetX: int32(theme.CalloutOffsetX),
		CalloutOffsetY: int32(theme.CalloutOffsetY), CalloutFields: append([]string(nil), theme.CalloutFields...),
		AttributionFontSize: int32(theme.AttributionFontSize), ShowAreaLabels: theme.ShowAreaLabels,
		ShowPoiLabels: theme.ShowPoiLabels,
	}
}
