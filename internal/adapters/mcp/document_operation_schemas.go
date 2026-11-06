package mcp

const documentActionTypeJSONSchema = `{"enum":["post","page","work","release","program_event"]}`

const documentBatchInputDocuments = `"documents":{"type":"array","minItems":1,"maxItems":50,"description":"The documents to act on. Each stands alone, so a failure in one leaves the others done and reports which.","items":{
      "type":"object","additionalProperties":false,"required":["document_type","document_id"],
      "properties":{"document_type":` + documentActionTypeJSONSchema + `,"document_id":` + documentReferenceJSONSchema + `}
    }}`

const documentPublishInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["action","documents"],
  "properties":{
    "action":{"enum":["publish","unpublish","archive","republish","schedule","cancel_schedule"],"description":"publish: every type; a published document is unchanged and an archived Program Event is restored. unpublish: back to draft, for post, page, work and release; a site Admin may also unpublish an archived Post. archive: post and program_event; archive does not move a Program Event to draft. republish, schedule, cancel_schedule: post only."},
    ` + documentBatchInputDocuments + `,
    "scheduled_at":{"type":"string","format":"date-time","description":"With schedule: the instant to publish at."},
    "scheduled_time_zone":{"type":"string","minLength":1,"maxLength":100,"description":"With schedule: an IANA time zone."}
  }
}`

const documentDeleteInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["documents"],
  "properties":{` + documentBatchInputDocuments + `}
}`

const documentOperationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["documents","applied","failed"],
  "properties":{
    "documents":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["document_type","document_id","status"],"properties":{
      "document_type":{"type":"string"},
      "document_id":{"type":"string"},
      "status":{"enum":["applied","failed"],"description":"failed means nothing changed in this document."},
      "changed":{"type":"boolean","description":"False when the document was already in that state."},
      "error":{"type":"string"}
    }}},
    "applied":{"type":"integer","minimum":0},
    "failed":{"type":"integer","minimum":0}
  }
}`

const documentSettingsGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id"],
  "properties":{"document_type":` + documentActionTypeJSONSchema + `,"document_id":` + documentReferenceJSONSchema + `}
}`

const documentSettingsGetOutputJSONSchema = `{"type":"object","description":"The document's settings, publication state and allowed actions, which differ by type. A Post's configuration_revision goes to post_settings_update; pass a Work's values as observed_metadata or observed_client_ids."}`

const documentSlugCheckInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","slug"],
  "properties":{
    "document_type":{"enum":["post","page","work","release"]},
    "slug":{"type":"string","minLength":1},
    "exclude_document_id":` + documentReferenceJSONSchema + `
  }
}`

const documentSlugCheckOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","slug","available"],
  "properties":{"document_type":{"enum":["post","page","work","release"]},"slug":{"type":"string"},"available":{"type":"boolean"}}
}`
