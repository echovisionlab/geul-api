package mcp

const policyTypeJSONSchema = `{"enum":["terms","privacy"]}`
const policyIDInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type","document_id"],
 "properties":{"document_type":` + policyTypeJSONSchema + `,"document_id":` + uuidJSONSchema + `}
}`
const policyCreateInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type"],
 "properties":{"document_type":` + policyTypeJSONSchema + `,"title":{"type":"string","minLength":1}}
}`
const policyListInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type"],
 "properties":{"document_type":` + policyTypeJSONSchema + `,"status":{"enum":["draft","scheduled","active","archived"]},"limit":{"type":"integer","minimum":1,"maximum":50,"default":20},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}}
}`
const policyRevisionInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type","document_id","expected_document_revision"],
 "properties":{"document_type":` + policyTypeJSONSchema + `,"document_id":` + uuidJSONSchema + `,"expected_document_revision":` + uuidJSONSchema + `}
}`
const policyScheduleInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type","document_id","expected_document_revision","effective_from"],
 "properties":{"document_type":` + policyTypeJSONSchema + `,"document_id":` + uuidJSONSchema + `,"expected_document_revision":` + uuidJSONSchema + `,"effective_from":{"type":"string","format":"date-time"}}
}`
const policyOutputJSONSchema = `{
 "type":"object","additionalProperties":false,
 "required":["document_type","document_id","version","title","source_locale","document_revision","status","effective_from","effective_until","created_at","updated_at"],
 "properties":{
  "document_type":` + policyTypeJSONSchema + `,"document_id":` + uuidJSONSchema + `,
  "version":{"type":"integer","minimum":1},"title":{"type":"string"},"source_locale":{"type":"string","minLength":1},"document_revision":` + uuidJSONSchema + `,
  "status":{"enum":["draft","scheduled","active","archived"]},"changed":{"type":"boolean"},
  "effective_from":{"type":["string","null"],"format":"date-time"},"effective_until":{"type":["string","null"],"format":"date-time"},
  "created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
 }
}`
const policyListOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["policies","total","next_offset"],
 "properties":{"policies":{"type":"array","items":` + policyOutputJSONSchema + `},"total":{"type":"integer","minimum":0},"next_offset":{"type":["integer","null"],"minimum":0}}
}`
const policyMutationOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type","document_id","changed"],
 "properties":{
  "document_type":` + policyTypeJSONSchema + `,"document_id":` + uuidJSONSchema + `,"changed":{"type":"boolean"},"deleted":{"type":"boolean"},
  "document_revision":` + uuidJSONSchema + `,"status":{"enum":["draft","scheduled","active","archived"]},
  "effective_from":{"type":["string","null"],"format":"date-time"},"effective_until":{"type":["string","null"],"format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
 }
}`
