package mcp

const documentTypeAndIDInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id"],
  "properties":{"document_type":{"enum":["post","work","page"]},"document_id":` + documentReferenceJSONSchema + `}
}`

const featuredImageSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","file_id"],
  "properties":{"document_type":{"enum":["post","work","page"]},"document_id":` + documentReferenceJSONSchema + `,"file_id":` + documentReferenceJSONSchema + `}
}`

const postParticipantInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","member_id"],
  "properties":{"document_id":` + documentReferenceJSONSchema + `,"member_id":` + documentReferenceJSONSchema + `}
}`

const workCreditGroupCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","name"],
  "properties":{"document_id":` + documentReferenceJSONSchema + `,"name":{"type":"string","minLength":1}}
}`

const workCreditGroupUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["group_id","name"],
  "properties":{"group_id":` + documentReferenceJSONSchema + `,"name":{"type":"string","minLength":1}}
}`

const workCreditGroupDeleteInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["group_id"],
  "properties":{"group_id":` + documentReferenceJSONSchema + `}
}`

const workCreditAddInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "oneOf":[{"required":["artist_id"]},{"required":["member_id"]},{"required":["name"]}],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,"group_id":` + documentReferenceJSONSchema + `,
    "artist_id":` + documentReferenceJSONSchema + `,"member_id":` + documentReferenceJSONSchema + `,
    "name":{"type":"string","minLength":1},"credit_role":{"type":"string","minLength":1}
  }
}`

const workCreditUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["credit_id"],
  "anyOf":[{"required":["group_id"]},{"required":["credit_role"]}],
  "properties":{"credit_id":` + documentReferenceJSONSchema + `,"group_id":{"type":"string"},"credit_role":{"type":"string"}}
}`

const workCreditDeleteInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["credit_id"],
  "properties":{"credit_id":` + documentReferenceJSONSchema + `}
}`

const documentVersionsListInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id"],
  "properties":{
    "document_type":{"enum":["post","work","page"]},"document_id":` + documentReferenceJSONSchema + `,
    "limit":{"type":"integer","minimum":1,"maximum":100,"default":20},"offset":{"type":"integer","minimum":0,"default":0}
  }
}`

const documentVersionRestoreInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","version_id"],
  "properties":{"document_type":{"enum":["post","work","page"]},"document_id":` + documentReferenceJSONSchema + `,"version_id":` + documentReferenceJSONSchema + `}
}`

const documentSlugCheckInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","slug"],
  "properties":{"document_type":{"enum":["post","work","page"]},"slug":{"type":"string","minLength":1},"exclude_document_id":` + documentReferenceJSONSchema + `}
}`

const contentActionOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["resource_type","resource_id","changed"],
  "properties":{
    "resource_type":{"type":"string"},"resource_id":{"type":"string"},"document_type":{"enum":["post","work","page"]},
    "document_id":` + documentReferenceJSONSchema + `,"changed":{"type":"boolean"},"deleted":{"type":"boolean"},
    "file_id":` + documentReferenceJSONSchema + `,"member_id":` + documentReferenceJSONSchema + `,
    "role":{"type":"string"},"name":{"type":"string"},"group_id":{"type":"string"},"credit_role":{"type":"string"},"artist_id":{"type":"string"},
    "og_generation_run_id":{"type":"string"},"document_revision":{"type":"string"},"title":{"type":"string"},"status":{"type":"string"}
  }
}`

const postParticipantsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","participants"],
  "properties":{"document_id":` + documentReferenceJSONSchema + `,"participants":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["member_id","nickname","role","has_effective_authority","deleted","created_at"],"properties":{"member_id":` + documentReferenceJSONSchema + `,"nickname":{"type":"string"},"role":{"enum":["author","collaborator"]},"has_effective_authority":{"type":"boolean"},"deleted":{"type":"boolean"},"created_at":{"type":"string","format":"date-time"}}}}}
}`

const workCreditsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","groups","credits"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "groups":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"}}}},
    "credits":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":` + documentReferenceJSONSchema + `,"group_id":{"type":"string"},"name":{"type":"string"},"credit_role":{"type":"string"},"artist_id":{"type":"string"},"artist_name":{"type":"string"},"member_id":{"type":"string"},"member_nickname":{"type":"string"}}}}
  }
}`

const documentVersionsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","versions","total","has_more"],
  "properties":{
    "document_type":{"enum":["post","work","page"]},"document_id":` + documentReferenceJSONSchema + `,
    "versions":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["version_id","version","title","canonical_hash","source_locale","created_at","contributors"],"properties":{"version_id":` + documentReferenceJSONSchema + `,"version":{"type":"integer"},"title":{"type":"string"},"summary":{"type":"string"},"canonical_hash":{"type":"string"},"source_locale":{"type":"string"},"created_at":{"type":"string","format":"date-time"},"contributors":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["member_id","nickname"],"properties":{"member_id":` + documentReferenceJSONSchema + `,"nickname":{"type":"string"}}}}}}},
    "total":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer"}
  }
}`

const documentSlugCheckOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","slug","available"],
  "properties":{"document_type":{"enum":["post","work","page"]},"slug":{"type":"string"},"available":{"type":"boolean"}}
}`
