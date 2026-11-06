package mcp

const documentListInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type"],
  "properties":{
    "document_type":{"allOf":[` + domainJSONSchema + `],"description":"The type of document to list."},
    "query":{"type":"string","maxLength":200,"description":"Optional case-insensitive source-title substring, or Artist source-name or real-name substring."},
    "status":{"type":"string","description":"Keep only this state, as the status field of a listed document reads, for the types that filter by it: label, form and post_series (draft, published); terms and privacy (draft, scheduled, active, archived)."},
    "limit":{"type":"integer","minimum":1,"maximum":100,"default":20},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}
  }
}`

const documentListOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["documents","total","next_offset"],
  "properties":{
    "documents":{"type":"array","items":{
      "type":"object","additionalProperties":false,
      "required":["document_type","document_id","title","source_locale"],
      "properties":{
        "document_type":` + domainJSONSchema + `,
        "document_id":` + uuidJSONSchema + `,
        "title":{"type":"string"},
        "slug":{"type":"string"},
        "source_locale":{"type":"string","minLength":1,"maxLength":35},
        "status":{"type":"string"},
        "updated_at":{"type":"string","format":"date-time"},
        "configuration_revision":{"type":"string","format":"uuid","description":"Post settings revision for post_settings_update. Separate from the content document revision."},
        "version":{"type":"integer","description":"terms and privacy only."},
        "document_revision":{"type":"string","description":"terms and privacy only."},
        "effective_from":{"type":["string","null"],"format":"date-time","description":"terms and privacy only."},
        "effective_until":{"type":["string","null"],"format":"date-time","description":"terms and privacy only."}
      },
      "allOf":[
        {"if":{"properties":{"document_type":{"const":"post"}},"required":["document_type"]},"then":{"required":["configuration_revision"]}},
        {"if":{"properties":{"document_type":{"enum":["post","work","page","program_event","release","artist"]}},"required":["document_type"]},"then":{"required":["status","updated_at"]}}
      ]
    }},
    "total":{"type":"integer","minimum":0},
    "next_offset":{"type":["integer","null"],"minimum":0}
  }
}`
