package mcp

const pageReferencePaginationProperties = `"limit":{"type":"integer","minimum":1,"maximum":100,"default":20},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}`

const formListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + pageReferencePaginationProperties + `,"query":{"type":"string","description":"Search canonical Form source titles; empty or omitted lists all matches."},"status":{"enum":["FORM_STATUS_DRAFT","FORM_STATUS_PUBLISHED"]}}
}`

const postSeriesListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + pageReferencePaginationProperties + `,"query":{"type":"string","description":"Search canonical Post series source titles; empty or omitted lists all matches."},"status":{"enum":["SERIES_STATUS_DRAFT","SERIES_STATUS_PUBLISHED"]}}
}`

const pageReferencePaginationOutputProperties = `"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer","description":"Present only when the native list reports has_more. Pass this offset with the same filters to continue."}`

const pageReferenceIDJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Canonical reference UUID; pass unchanged as form_id or series_id as appropriate. Never a slug or URL."}`

const formListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + pageReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","title","status","source_locale"],"properties":{
      "id":` + pageReferenceIDJSONSchema + `,"title":{"type":"string","description":"Form title in its source locale."},"slug":{"type":"string"},"status":{"enum":["FORM_STATUS_UNSPECIFIED","FORM_STATUS_DRAFT","FORM_STATUS_PUBLISHED"],"description":"Native status snapshot; publication alone does not guarantee public form access."},"source_locale":{"type":"string"}
    }}}
  }
}`

const postSeriesListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + pageReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","title","slug","status","source_locale","post_count"],"properties":{
      "id":` + pageReferenceIDJSONSchema + `,"title":{"type":"string","description":"Post series title in its source locale."},"slug":{"type":"string"},"status":{"enum":["SERIES_STATUS_DRAFT","SERIES_STATUS_PUBLISHED"]},"source_locale":{"type":"string"},"post_count":{"type":"integer","description":"Native count of associated posts, including drafts; does not guarantee public section results."}
    }}}
  }
}`
