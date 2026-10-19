package mcp

const adminSettingsEmptyInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{}}`

// Value, reference and attachment checks belong to the settings service.
const siteSettingsMutablePropertiesJSONSchema = `
  "site_title":{"type":["string","null"]},
  "company_name":{"type":["string","null"]},
  "company_address":{"type":["string","null"]},
  "tax_id":{"type":["string","null"]},
  "legal_email":{"type":["string","null"],"description":"Email address, or empty/null to clear."},
  "contact_email":{"type":["string","null"],"description":"Unified contact email address, or empty/null to clear. Legal email remains separate."},
  "social_links":{"type":["object","null"],"description":"Replaces the full social links object; null resets it to an empty object."},
  "logo_full_light_file_id":{"type":["string","null"],"description":"Full logo (symbol with wordmark) drawn on light backgrounds: site header, drawer, admin navigation, print, OG and structured data. Source SVG or PNG File UUID from file_upload kind=image or file_list; empty/null clears."},
  "logo_full_dark_file_id":{"type":["string","null"],"description":"Full logo drawn on dark backgrounds. Source SVG or PNG File UUID; empty/null clears. When unset, the light full logo is shown."},
  "logo_symbol_light_file_id":{"type":["string","null"],"description":"Symbol-only logo drawn on light backgrounds, e.g. the collapsed admin navigation. Source SVG or PNG File UUID; empty/null clears. When no symbol is set, the full logo is shown."},
  "logo_symbol_dark_file_id":{"type":["string","null"],"description":"Symbol-only logo drawn on dark backgrounds. Source SVG or PNG File UUID; empty/null clears. When unset, the light symbol is shown."},
  "logo_email_light_file_id":{"type":["string","null"],"description":"Email logo for light mail (template variable logo_email_url). Source PNG File UUID only; empty/null clears. When unset, the dark email logo is used."},
  "logo_email_dark_file_id":{"type":["string","null"],"description":"Email logo for dark mail clients (template variable logo_email_dark_url). Source PNG File UUID only; empty/null clears. When unset, the light email logo is used."},
  "favicon_file_id":{"type":["string","null"],"description":"Source favicon File UUID; empty/null clears. Do not use a rendered favicon asset ID."},
  "site_og_background_file_id":{"type":["string","null"]},
  "privacy_og_background_file_id":{"type":["string","null"]},
  "terms_og_background_file_id":{"type":["string","null"]},
  "primary_color":{"type":"string","pattern":"^#[0-9A-Fa-f]{6}$","description":"Six-digit hex color including #."},
  "default_comments_enabled":{"type":["boolean","null"],"description":"Null resets to false."},
  "homepage_page_id":{"type":["string","null"],"description":"Page UUID from document_list p=page; empty/null clears."},
  "menu_header_id":{"type":["string","null"],"description":"Menu UUID from menu_list; empty/null clears."},
  "menu_secondary_id":{"type":["string","null"],"description":"Menu UUID from menu_list; empty/null clears."},
  "menu_footer_id":{"type":["string","null"],"description":"Menu UUID from menu_list; empty/null clears."},
  "menu_avatar_dropdown_id":{"type":["string","null"],"description":"Menu UUID from menu_list; empty/null clears."},
  "meta_description":{"type":["string","null"]},
  "google_analytics_id":{"type":["string","null"],"description":"G-, UA-, or GT- identifier with uppercase alphanumeric suffix; empty/null clears."},
  "og_image_config":{"type":["object","null"],"description":"Replaces the complete OG authoring config; null clears."}
`

const siteSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["settings"],
  "properties":{"settings":{"type":"object","additionalProperties":false,"minProperties":1,"properties":{` + siteSettingsMutablePropertiesJSONSchema + `,
    "og_image_config.home":{"type":["object","null"],"description":"Replaces only the home section; null resets to an empty object."},
    "og_image_config.content":{"type":["object","null"],"description":"Replaces only the content section; null resets to an empty object."}
  }}}
}`

const siteSettingsGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["settings"],
  "properties":{"settings":{"type":"object","additionalProperties":false,"properties":{` + siteSettingsMutablePropertiesJSONSchema + `,
    "loader_file_ids":{"type":"array","items":{"type":"string"},"description":"Read-only ordered source File IDs."},
    "default_map_theme_id":{"type":"string","description":"Read-only here. Change with map_theme_set_default."},
    "site_origin":{"type":"string","description":"Read-only runtime origin."}
  }}}
}`

const siteSettingsUpdateOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["success","og_generation_run_id"],
  "properties":{"success":{"type":"boolean"},"og_generation_run_id":{"type":["string","null"],"description":"OG generation run queued by the owning service, if any."}}
}`

const mailAdapterListInputJSONSchema = `{
  "type":"object","additionalProperties":false,"properties":{
    "limit":{"type":"integer","minimum":1,"maximum":100,"default":20},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}
  }
}`

const mailAdapterAdminOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["id","name","type","is_active","priority","from_name","from_email"],
  "properties":{
    "id":` + uuidJSONSchema + `,"name":{"type":"string"},"type":{"enum":["unspecified","logging","ses","smtp"]},
    "is_active":{"type":"boolean"},"priority":{"type":"integer"},"from_name":{"type":"string"},"from_email":{"type":"string"},
    "region":{"type":"string"},"host":{"type":"string"},"port":{"type":"integer"},"secure":{"type":"boolean"},
    "created_at":{"type":"string"},"updated_at":{"type":"string"}
  }
}`

const mailAdapterListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["adapters","total","limit","offset","has_more"],
  "properties":{"adapters":{"type":"array","items":` + mailAdapterAdminOutputJSONSchema + `},"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer"}}
}`

const mailAdapterSenderUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["id"],
  "properties":{
    "id":{"description":"Mail adapter UUID from mail_adapter_list.","allOf":[` + uuidJSONSchema + `]},
    "from_name":{"type":"string","description":"Sender display name. Empty clears it; omission preserves it."},
    "from_email":{"type":"string","minLength":1,"description":"Sender address validated and verified by the existing admin service."},
    "is_active":{"type":"boolean"},"priority":{"type":"integer","minimum":-2147483648,"maximum":2147483647}
  }
}`

const mailAdapterSenderUpdateOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["adapter"],
  "properties":{"adapter":` + mailAdapterAdminOutputJSONSchema + `}
}`
