package mcp

const fileManagerFileReferenceJSONSchema = `{"allOf":[` + uuidJSONSchema + `],"description":"DSUB File UUID returned by file_upload or file_list."}`
const fileManagerFolderReferenceJSONSchema = `{"allOf":[` + uuidJSONSchema + `],"description":"File Manager folder UUID returned by file_list or file_folder_create."}`
const fileManagerNullableIDJSONSchema = `{"anyOf":[` + uuidJSONSchema + `,{"type":"null"}],"description":"File Manager folder UUID returned by file_list or file_folder_create; omit or pass null for the virtual root."}`
const fileManagerIDsJSONSchema = `{"type":"array","minItems":1,"maxItems":100,"description":"DSUB File UUIDs returned by file_upload or file_list.","items":` + uuidJSONSchema + `}`
const fileManagerNameJSONSchema = `{"type":"string","minLength":1,"maxLength":255}`
const fileManagerIDsInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file_ids"],
 "properties":{"file_ids":` + fileManagerIDsJSONSchema + `}
}`
const fileManagerMoveInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file_ids"],
 "properties":{"file_ids":` + fileManagerIDsJSONSchema + `,"folder_id":` + fileManagerNullableIDJSONSchema + `}
}`
const fileManagerRenameInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file_id","file_name"],
 "properties":{"file_id":` + fileManagerFileReferenceJSONSchema + `,"file_name":` + fileManagerNameJSONSchema + `}
}`
const fileManagerFolderCreateInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["name"],
 "properties":{"name":` + fileManagerNameJSONSchema + `,"parent_id":` + fileManagerNullableIDJSONSchema + `}
}`
const fileManagerFolderRenameInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["folder_id","name"],
 "properties":{"folder_id":` + fileManagerFolderReferenceJSONSchema + `,"name":` + fileManagerNameJSONSchema + `}
}`
const fileManagerFolderMoveInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["folder_id"],
 "properties":{"folder_id":` + fileManagerFolderReferenceJSONSchema + `,"parent_id":` + fileManagerNullableIDJSONSchema + `}
}`
const fileManagerFolderIDInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["folder_id"],
 "properties":{"folder_id":` + fileManagerFolderReferenceJSONSchema + `}
}`
const fileManagerFileJSONSchema = `{
 "type":"object","additionalProperties":false,
 "required":["id","file_name","extension","mime_type","file_size","folder_id","usage_count","created_at","updated_at"],
 "properties":{
  "id":` + uuidJSONSchema + `,"file_name":{"type":"string"},"extension":{"type":"string"},
  "mime_type":{"type":"string"},"file_size":{"type":"integer"},"duration_seconds":{"type":"integer"},
  "folder_id":` + fileManagerNullableIDJSONSchema + `,"usage_count":{"type":"integer"},
  "created_at":{"type":"string"},"updated_at":{"type":"string"}
 }
}`
const fileManagerFolderJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["id","parent_id","name","created_at","updated_at"],
 "properties":{"id":` + uuidJSONSchema + `,"parent_id":` + fileManagerNullableIDJSONSchema + `,
 "name":{"type":"string"},"created_at":{"type":"string"},"updated_at":{"type":"string"}}
}`
const fileManagerDomainJSONSchema = `{"enum":["post","page","work","site_settings","release","track","artist","label","client","series","form","program_event","map_place"]}`
const fileManagerUsageJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["domain","entity_id","reference_path","count"],
 "properties":{"domain":` + fileManagerDomainJSONSchema + `,"entity_id":{"type":"string"},"reference_path":{"type":"string"},
 "count":{"type":"integer"},"block_id":{"type":"string"},"block_type":{"type":"string"},"title":{"type":"string"},"link":{"type":"string"}}
}`
const fileManagerImpactJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file_id","total_usage_count","domain_counts","first_usages","has_more_usages"],
 "properties":{
  "file_id":` + uuidJSONSchema + `,"total_usage_count":{"type":"integer"},
  "domain_counts":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["domain","count"],"properties":{"domain":` + fileManagerDomainJSONSchema + `,"count":{"type":"integer"}}}},
  "first_usages":{"type":"array","maxItems":5,"items":` + fileManagerUsageJSONSchema + `},"has_more_usages":{"type":"boolean"}
 }
}`
const fileManagerImpactOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["impacts"],
 "properties":{"impacts":{"type":"array","items":` + fileManagerImpactJSONSchema + `}}
}`
const fileManagerDeleteOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["accepted_file_ids","rejected_files"],
 "properties":{"accepted_file_ids":{"type":"array","items":` + uuidJSONSchema + `},"rejected_files":{"type":"array","items":` + fileManagerImpactJSONSchema + `}}
}`
const fileManagerFolderDeleteOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["folder_id","accepted_file_ids"],
 "properties":{"folder_id":` + uuidJSONSchema + `,"accepted_file_ids":{"type":"array","items":` + uuidJSONSchema + `}}
}`
const fileManagerFileOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file"],"properties":{"file":` + fileManagerFileJSONSchema + `}
}`
const fileManagerFilesOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["files"],"properties":{"files":{"type":"array","items":` + fileManagerFileJSONSchema + `}}
}`
const fileManagerFolderOutputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["folder"],"properties":{"folder":` + fileManagerFolderJSONSchema + `}
}`
