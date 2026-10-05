package mcp

const musicReferenceListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{
    "query":{"type":"string","description":"Search the native reference name; empty or omitted lists all matches."},
    "limit":{"type":"integer","minimum":1,"maximum":100,"default":20},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}
  }
}`

const musicReferencePaginationOutputProperties = `"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer","description":"Present only when the native list reports has_more. Continue with this offset and the same query."}`

const musicReferenceItemProperties = `"id":{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Canonical Genre, Style, or Format UUID. Pass unchanged in the matching Release relation; never a slug or URL."},"name":{"type":"string","description":"Native catalog name; this reference contract has no locale selection."},"slug":{"type":"string"}`

const musicNamedReferenceListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + musicReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","slug"],"properties":{` + musicReferenceItemProperties + `,"description":{"type":"string"}}}}
  }
}`

const musicFormatListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + musicReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","slug"],"properties":{` + musicReferenceItemProperties + `}}}
  }
}`
