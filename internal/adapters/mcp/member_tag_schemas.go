package mcp

const memberTagListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string","description":"Search Member tag names."},"limit":{"type":"integer","minimum":1,"maximum":500,"default":50},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}}
}`

const memberTagListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","member_count"],"properties":{"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"},"member_count":{"type":"integer"}}}},
    "total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer"}
  }
}`
