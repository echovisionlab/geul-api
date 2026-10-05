package mcp

const contentIDInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "properties":{"document_id":` + documentReferenceJSONSchema + `}
}`

const postCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["title","source_locale"],
  "properties":{
    "title":{"type":"string","minLength":1},
    "source_locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "slug":{"type":"string"},"summary":{"type":"string"},
    "comments_enabled":{"type":"boolean","default":true},
    "category_ids":{"type":"array","maxItems":256,"uniqueItems":true,"items":` + documentReferenceJSONSchema + `},
    "tag_ids":{"type":"array","maxItems":256,"uniqueItems":true,"items":` + documentReferenceJSONSchema + `},
    "map_place_id":` + documentReferenceJSONSchema + `
  }
}`

const postSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","expected_configuration_revision"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "expected_configuration_revision":{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Exact current Post settings revision from document_list or post_create. This is separate from document_revision; preserve it exactly and reload after a stale-write error."},
    "slug":{"type":"string","description":"New slug, or an empty string to remove the slug."},
    "comments_enabled":{"type":"boolean"},
    "map_place_id":{"type":"string","description":"Canonical Map Place UUID, or an empty string to remove the relation."}
  }
}`

const postConfigurationMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","changed","configuration_revision"],
  "properties":{
    "document_type":{"const":"post"},
    "document_id":` + documentReferenceJSONSchema + `,
    "changed":{"type":"boolean"},"deleted":{"type":"boolean"},
    "title":{"type":"string"},"slug":{"type":"string"},
    "source_locale":{"type":"string"},"status":{"type":"string"},
    "document_revision":{"type":"string","description":"Content document revision. Separate from the Post settings revision."},
    "configuration_revision":{"type":"string","format":"uuid","description":"Persisted Post settings revision to supply as expected_configuration_revision on the next settings update."},
    "updated_at":{"type":"string","format":"date-time"},
    "scheduled_at":{"type":"string","format":"date-time"},"scheduled_time_zone":{"type":"string"}
  }
}`

const postScheduleInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","scheduled_at","scheduled_time_zone"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "scheduled_at":{"type":"string","format":"date-time"},
    "scheduled_time_zone":{"type":"string","minLength":1,"maxLength":100}
  }
}`

const workCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["title","source_locale","type","year","month"],
  "properties":{
    "title":{"type":"string","minLength":1},
    "source_locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "slug":{"type":"string"},"summary":{"type":"string"},
    "type":{"enum":["music_project","portfolio","article","contribution"]},
    "metadata":{"type":"object"},"featured":{"type":"boolean"},
    "year":{"type":"integer","minimum":1,"maximum":9999},
    "month":{"type":"integer","minimum":1,"maximum":12},
    "until_year":{"type":"integer","minimum":1,"maximum":9999},
    "until_month":{"type":"integer","minimum":1,"maximum":12},
    "is_present":{"type":"boolean","description":"When true, until_year and until_month must be omitted. Otherwise both fields are required."}
  },
  "oneOf":[
    {"required":["is_present"],"properties":{"is_present":{"const":true}},"not":{"anyOf":[{"required":["until_year"]},{"required":["until_month"]}]}},
    {"required":["until_year","until_month"],"properties":{"is_present":{"const":false}}}
  ]
}`

const workSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "slug":{"type":"string","description":"New slug, or an empty string to remove the slug."},
    "type":{"enum":["music_project","portfolio","article","contribution"]},
    "metadata":{"type":"object"},
    "observed_metadata":{"type":"object","description":"Copy metadata returned by work_settings_get before editing. Preserve unchanged fields to keep concurrent peer edits."},
    "featured":{"type":"boolean"},
    "client_ids":{"type":"array","maxItems":256,"uniqueItems":true,"items":` + documentReferenceJSONSchema + `},
    "observed_client_ids":{"type":"array","maxItems":256,"uniqueItems":true,"items":` + documentReferenceJSONSchema + `,"description":"Copy client_ids returned by work_settings_get before editing, including an empty array when no clients were present."},
    "year":{"type":"integer","minimum":1,"maximum":9999},
    "month":{"type":"integer","minimum":1,"maximum":12},
    "map_place_id":{"type":"string","description":"Canonical Map Place UUID, or an empty string to remove the relation."},
    "until_year":{"type":"integer","minimum":1,"maximum":9999},
    "until_month":{"type":"integer","minimum":1,"maximum":12},
    "is_present":{"type":"boolean","description":"Setting true clears the stored until date. Setting false for a currently present Work requires until_year and until_month in the same call."}
  },
  "allOf":[
    {"if":{"required":["metadata"]},"then":{"required":["observed_metadata"]}},
    {"if":{"required":["client_ids"]},"then":{"required":["observed_client_ids"]}}
  ]
}`

const workSettingsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","metadata","client_ids","type","featured","year","month","is_present","document_revision"],
  "properties":{
    "document_type":{"const":"work"},"document_id":` + documentReferenceJSONSchema + `,
    "metadata":{"type":"object"},"client_ids":{"type":"array","items":` + documentReferenceJSONSchema + `},
    "slug":{"type":"string"},"type":{"enum":["music_project","portfolio","article","contribution"]},
    "featured":{"type":"boolean"},"year":{"type":"integer"},"month":{"type":"integer"},
    "until_year":{"type":"integer"},"until_month":{"type":"integer"},"is_present":{"type":"boolean"},
    "map_place_id":` + documentReferenceJSONSchema + `,
    "document_revision":{"type":"string","description":"Content document revision; Work settings edits use observed metadata/client values instead of this revision."}
  }
}`

const pageCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["title","source_locale"],
  "properties":{
    "title":{"type":"string","minLength":1},
    "source_locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "slug":{"type":"string"},"summary":{"type":"string"},"show_title":{"type":"boolean"}
  }
}`

const pageSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "slug":{"type":"string","description":"New slug, or an empty string to remove the slug."},
    "show_title":{"type":"boolean"}
  }
}`

const contentMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","changed"],
  "properties":{
    "document_type":{"enum":["post","work","page"]},
    "document_id":` + documentReferenceJSONSchema + `,
    "changed":{"type":"boolean"},"deleted":{"type":"boolean"},
    "title":{"type":"string"},"slug":{"type":"string"},
    "source_locale":{"type":"string"},"status":{"type":"string"},
    "document_revision":{"type":"string"},"updated_at":{"type":"string","format":"date-time"},
    "scheduled_at":{"type":"string","format":"date-time"},"scheduled_time_zone":{"type":"string"}
  }
}`
