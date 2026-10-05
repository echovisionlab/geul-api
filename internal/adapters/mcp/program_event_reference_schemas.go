package mcp

const programEventReferencePaginationProperties = `"limit":{"type":"integer","minimum":1,"maximum":100,"default":20},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}`

const programEventTypeListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + programEventReferencePaginationProperties + `,"status":{"enum":["PROGRAM_EVENT_TYPE_STATUS_ACTIVE","PROGRAM_EVENT_TYPE_STATUS_INACTIVE"]}}
}`

const programEventSeriesListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + programEventReferencePaginationProperties + `,"query":{"type":"string","description":"Search the canonical series title; empty or omitted lists all matches."},"status":{"enum":["PROGRAM_EVENT_SERIES_STATUS_DRAFT","PROGRAM_EVENT_SERIES_STATUS_PUBLISHED"]}}
}`

const labelListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + programEventReferencePaginationProperties + `,"query":{"type":"string","description":"Search Label source names; empty or omitted lists all matches."},"status":{"enum":["LABEL_STATUS_DRAFT","LABEL_STATUS_PUBLISHED"]}}
}`

const programEventReferencePaginationOutputProperties = `"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer","description":"Present only when the native list reports has_more. Pass this offset with the same filters to continue."}`

const programEventReferenceIDJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Canonical reference UUID; pass unchanged as type_id, series_id, or a label_id as appropriate. Never a slug or URL."}`

const programEventTypeListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + programEventReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","slug","status","requires_place","requires_stream_url","locales"],"properties":{
      "id":` + programEventReferenceIDJSONSchema + `,"slug":{"type":"string"},"status":{"enum":["PROGRAM_EVENT_TYPE_STATUS_UNSPECIFIED","PROGRAM_EVENT_TYPE_STATUS_ACTIVE","PROGRAM_EVENT_TYPE_STATUS_INACTIVE"]},
      "requires_place":{"type":"boolean","description":"The type's configured place requirement; use map_place_id for a place relation."},"requires_stream_url":{"type":"boolean","description":"The type's configured stream requirement; use stream_url for a streaming URL."},
      "locales":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["locale","name"],"properties":{"locale":{"type":"string"},"name":{"type":"string"},"description":{"type":"string"}}}}
    }}}
  }
}`

const programEventSeriesListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + programEventReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","slug","title","status"],"properties":{"id":` + programEventReferenceIDJSONSchema + `,"slug":{"type":"string"},"title":{"type":"string","description":"Canonical series title."},"status":{"enum":["PROGRAM_EVENT_SERIES_STATUS_UNSPECIFIED","PROGRAM_EVENT_SERIES_STATUS_DRAFT","PROGRAM_EVENT_SERIES_STATUS_PUBLISHED"]}}}}
  }
}`

const labelListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + programEventReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","status","source_locale"],"properties":{"id":` + programEventReferenceIDJSONSchema + `,"name":{"type":"string","description":"Label name in its source locale."},"slug":{"type":"string"},"status":{"enum":["LABEL_STATUS_DRAFT","LABEL_STATUS_PUBLISHED"]},"source_locale":{"type":"string"}}}}
  }
}`
