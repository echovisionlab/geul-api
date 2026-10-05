package mcp

const programEventMediaRoleJSONSchema = `{"enum":["poster","gallery","lineup","sponsor","social","venue"],"default":"poster"}`

const programEventMediaItemJSONSchema = `{
 "type":"object","additionalProperties":false,
 "required":["id","file_id","role","sort_order","is_primary"],
 "properties":{
  "id":` + uuidJSONSchema + `,"file_id":` + uuidJSONSchema + `,
  "role":` + programEventMediaRoleJSONSchema + `,"sort_order":{"type":"integer"},"is_primary":{"type":"boolean"},
  "alt":{"type":"string"},"caption":{"type":"string"},
  "created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
 }
}`

const programEventMediaListOutputJSONSchema = `{
 "type":"object","additionalProperties":false,
 "required":["document_type","document_id","source_locale","updated_at","media"],
 "properties":{
  "document_type":{"const":"program_event"},"document_id":` + uuidJSONSchema + `,
  "source_locale":{"type":"string"},"updated_at":{"type":"string","format":"date-time","description":"Observed native event timestamp; media APIs do not accept it as a CAS token."},
  "media":{"type":"array","items":` + programEventMediaItemJSONSchema + `}
 }
}`

const programEventMediaAddInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_id","file_id"],
 "properties":{
  "document_id":` + documentReferenceJSONSchema + `,"file_id":{"description":"File UUID from file_list, file_upload, or file_transfer.","allOf":[` + uuidJSONSchema + `]},
  "role":` + programEventMediaRoleJSONSchema + `,"alt":{"type":"string"},"caption":{"type":"string"},
  "make_primary":{"type":"boolean","default":false}
 }
}`

const programEventMediaRemoveInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_id","media_id"],
 "properties":{"document_id":` + documentReferenceJSONSchema + `,"media_id":{"description":"Media row UUID from program_event_media_list for this event.","allOf":[` + uuidJSONSchema + `]}}
}`

const programEventMediaReorderInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_id","media_ids"],
 "properties":{
  "document_id":` + documentReferenceJSONSchema + `,"role":` + programEventMediaRoleJSONSchema + `,
  "media_ids":{"type":"array","uniqueItems":true,"description":"Every media row ID from program_event_media_list for exactly this event and role, in desired order. Empty is valid only when that role has no media.","items":` + uuidJSONSchema + `}
 }
}`

const programEventMediaMutationOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["document_type","document_id","changed","updated_at","next_read"],
 "properties":{
  "document_type":{"const":"program_event"},"document_id":` + uuidJSONSchema + `,
  "changed":{"type":"boolean"},"updated_at":{"type":"string","format":"date-time"},
  "media":` + programEventMediaItemJSONSchema + `,"media_id":` + uuidJSONSchema + `,
  "role":` + programEventMediaRoleJSONSchema + `,"media_ids":{"type":"array","items":` + uuidJSONSchema + `},
  "next_read":{"type":"object","additionalProperties":false,"required":["tool","arguments"],"properties":{
   "tool":{"const":"program_event_media_list"},"arguments":{"type":"object","additionalProperties":false,"required":["document_id"],"properties":{"document_id":` + uuidJSONSchema + `}}
  }}
 }
}`
