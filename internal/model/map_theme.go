package model

import (
	"time"

	"github.com/lib/pq"
)

// MapTheme represents a map theme
// Maps to: sql/001_schema.sql - map_theme table
type MapTheme struct {
	ID                  string         `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name                string         `gorm:"column:name;type:varchar(255);not null"`
	CalloutScale        float32        `gorm:"column:callout_scale;not null;default:1"`
	CalloutOffsetX      int            `gorm:"column:callout_offset_x;not null;default:0"`
	CalloutOffsetY      int            `gorm:"column:callout_offset_y;not null;default:0"`
	CalloutFields       pq.StringArray `gorm:"column:callout_fields;type:text[];not null;default:ARRAY['name', 'address']"`
	AttributionFontSize int            `gorm:"column:attribution_font_size;not null;default:11"`
	ShowAreaLabels      bool           `gorm:"column:show_area_labels;not null;default:true"`
	ShowPoiLabels       bool           `gorm:"column:show_poi_labels;not null;default:false"`
	EditVersion         int64          `gorm:"column:edit_version;not null;default:1"`
	CreatedAt           time.Time      `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt           time.Time      `gorm:"column:updated_at;not null;default:now()"`

	// A Theme is one durable aggregate. Light and dark are fixed fields of that
	// aggregate, not independently addressable rows.
	LightVariant MapThemeVariant `gorm:"embedded;embeddedPrefix:light_"`
	DarkVariant  MapThemeVariant `gorm:"embedded;embeddedPrefix:dark_"`
}

// TableName returns the table name for GORM
func (MapTheme) TableName() string {
	return "map_theme"
}

// MapThemeVariant is the typed light/dark portion embedded in MapTheme.
// It is not an independently persisted entity.
type MapThemeVariant struct {
	// ID and Scheme are transport-only; the database stores fixed light_ and
	// dark_ fields on MapTheme instead of child rows.
	ID                           string `gorm:"-"`
	Scheme                       string `gorm:"-"`
	BackgroundColor              string `gorm:"column:background_color;type:varchar(50);not null"`
	WaterColor                   string `gorm:"column:water_color;type:varchar(50);not null"`
	LandColor                    string `gorm:"column:land_color;type:varchar(50);not null"`
	RoadColor                    string `gorm:"column:road_color;type:varchar(50);not null"`
	BuildingFillColor            string `gorm:"column:building_fill_color;type:varchar(50);not null"`
	BuildingStrokeEnabled        bool   `gorm:"column:building_stroke_enabled;not null;default:false"`
	BuildingStrokeColor          string `gorm:"column:building_stroke_color;type:varchar(50);not null"`
	CalloutLineColor             string `gorm:"column:callout_line_color;type:varchar(50);not null"`
	CalloutTextColor             string `gorm:"column:callout_text_color;type:varchar(50);not null"`
	CalloutBackgroundColor       string `gorm:"column:callout_background_color;type:varchar(50);not null"`
	CalloutDescriptionColor      string `gorm:"column:callout_description_color;type:varchar(50);not null;default:'rgba(107,114,128,0.8)'"`
	AttributionColor             string `gorm:"column:attribution_color;type:varchar(50);not null;default:'rgba(128,128,128,0.55)'"`
	LabelTextColor               string `gorm:"column:label_text_color;type:varchar(50);not null;default:''"`
	ClusterColor                 string `gorm:"column:cluster_color;type:varchar(50);not null;default:''"`
	ClusterHoverColor            string `gorm:"column:cluster_hover_color;type:varchar(50);not null;default:''"`
	ClusterTextColor             string `gorm:"column:cluster_text_color;type:varchar(50);not null;default:''"`
	ClusterTextHoverColor        string `gorm:"column:cluster_text_hover_color;type:varchar(50);not null;default:''"`
	CalloutHoverLineColor        string `gorm:"column:callout_hover_line_color;type:varchar(50);not null;default:''"`
	CalloutHoverTextColor        string `gorm:"column:callout_hover_text_color;type:varchar(50);not null;default:''"`
	CalloutHoverDescriptionColor string `gorm:"column:callout_hover_description_color;type:varchar(50);not null;default:''"`
	CalloutHoverBackgroundColor  string `gorm:"column:callout_hover_background_color;type:varchar(50);not null;default:''"`
}
