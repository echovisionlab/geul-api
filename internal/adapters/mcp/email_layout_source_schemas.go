package mcp

const emailLayoutSourceGetInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["layout_id"],
 "properties":{"layout_id":` + uuidJSONSchema + `}
}`
const emailLayoutSourceReplaceInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["layout_id","expected_document_revision","find","replace"],
 "properties":{
  "layout_id":` + uuidJSONSchema + `,"expected_document_revision":` + uuidJSONSchema + `,
  "find":{"type":"string","minLength":1,"maxLength":65536,"description":"Exact source_markup substring that must occur exactly once."},
  "replace":{"type":"string","maxLength":65536,"description":"Replacement markup. Keep every <!--geul-unit:...--> marker and the text it introduces unchanged."}
 }
}`
const emailLayoutSourceOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["layout_id","source_locale","document_revision","source_markup"],
 "properties":{"layout_id":` + uuidJSONSchema + `,"source_locale":{"type":"string","minLength":1},"document_revision":` + uuidJSONSchema + `,"source_markup":{"type":"string"}}
}`
const emailLayoutSourceReplaceOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["layout_id","document_revision","changed"],
 "properties":{"layout_id":` + uuidJSONSchema + `,"document_revision":` + uuidJSONSchema + `,"changed":{"type":"boolean"}}
}`
