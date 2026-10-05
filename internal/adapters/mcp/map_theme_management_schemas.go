package mcp

const mapThemeSettingsJSONSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "callout_scale",
    "callout_fields",
    "attribution_font_size"
  ],
  "properties": {
    "callout_scale": {
      "type": "number",
      "minimum": 0.5,
      "maximum": 2
    },
    "callout_offset_x": {
      "type": "integer",
      "minimum": -50,
      "maximum": 50
    },
    "callout_offset_y": {
      "type": "integer",
      "minimum": -50,
      "maximum": 50
    },
    "callout_fields": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "enum": [
          "name",
          "address",
          "coordinates",
          "street",
          "city",
          "region",
          "country",
          "postalCode"
        ]
      }
    },
    "attribution_font_size": {
      "type": "integer",
      "minimum": 9,
      "maximum": 14
    },
    "show_area_labels": {
      "type": "boolean"
    },
    "show_poi_labels": {
      "type": "boolean"
    }
  }
}`

const mapThemeVariantJSONSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": [
    "background_color",
    "water_color",
    "land_color",
    "road_color",
    "building_fill_color",
    "building_stroke_color",
    "callout_line_color",
    "callout_text_color",
    "callout_background_color",
    "callout_description_color",
    "attribution_color",
    "label_text_color",
    "cluster_color",
    "cluster_hover_color",
    "cluster_text_color",
    "cluster_text_hover_color",
    "callout_hover_line_color",
    "callout_hover_text_color",
    "callout_hover_description_color",
    "callout_hover_background_color"
  ],
  "properties": {
    "background_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "water_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "land_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "road_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "building_fill_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "building_stroke_enabled": {
      "type": "boolean"
    },
    "building_stroke_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_line_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_text_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_background_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_description_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "attribution_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "label_text_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "cluster_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "cluster_hover_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "cluster_text_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "cluster_text_hover_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_hover_line_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_hover_text_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_hover_description_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    },
    "callout_hover_background_color": {
      "type": "string",
      "minLength": 1,
      "maxLength": 50
    }
  }
}`

const mapThemeSnapshotInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["name","settings","light_variant","dark_variant"],
 "properties":{"name":{"type":"string","minLength":1,"maxLength":255},"settings":` + mapThemeSettingsJSONSchema + `,"light_variant":` + mapThemeVariantJSONSchema + `,"dark_variant":` + mapThemeVariantJSONSchema + `}
}`

const mapThemeThemeJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["id","revision","snapshot","created_at","updated_at"],
 "properties":{"id":` + uuidJSONSchema + `,"revision":{"type":"integer","minimum":1},"snapshot":` + mapThemeSnapshotInputJSONSchema + `,"created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}}
}`

const mapThemeListInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{}}`
const mapThemeIDInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme_id"],"properties":{"theme_id":{"description":"Map Theme UUID from map_theme_list or map_theme_create.","allOf":[` + uuidJSONSchema + `]}}}`
const mapThemeCopyInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme_id","name"],"properties":{"theme_id":{"description":"Map Theme UUID from map_theme_list or map_theme_create.","allOf":[` + uuidJSONSchema + `]},"name":{"type":"string","minLength":1,"maxLength":255}}}`
const mapThemeResolveInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{"theme_id":{"description":"Map Theme UUID from map_theme_list or map_theme_create.","allOf":[` + uuidJSONSchema + `]},"scheme":{"enum":["light","dark"],"default":"light"}}}`
const mapThemeUpdateInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme_id","expected_revision","snapshot"],"properties":{"theme_id":{"description":"Map Theme UUID from map_theme_list or map_theme_create.","allOf":[` + uuidJSONSchema + `]},"expected_revision":{"type":"integer","minimum":1},"snapshot":` + mapThemeSnapshotInputJSONSchema + `}}`
const mapThemeOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme"],"properties":{"theme":` + mapThemeThemeJSONSchema + `}}`
const mapThemeUpdateOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme","changed"],"properties":{"theme":` + mapThemeThemeJSONSchema + `,"changed":{"type":"boolean"}}}`
const mapThemeListOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["themes","default_map_theme_id"],"properties":{"themes":{"type":"array","items":` + mapThemeThemeJSONSchema + `},"default_map_theme_id":` + uuidJSONSchema + `}}`
const mapThemeResolveOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme_id","scheme","settings","variant"],"properties":{"theme_id":` + uuidJSONSchema + `,"scheme":{"enum":["light","dark"]},"settings":` + mapThemeSettingsJSONSchema + `,"variant":` + mapThemeVariantJSONSchema + `}}`
const mapThemeDeleteOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["theme_id","success"],"properties":{"theme_id":` + uuidJSONSchema + `,"success":{"type":"boolean"}}}`
const mapThemeDefaultOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["default_map_theme_id"],"properties":{"default_map_theme_id":` + uuidJSONSchema + `}}`
