package mcp

const trackNullableUUIDJSONSchema = `{"type":["string","null"],"format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"}`
const trackInt32JSONSchema = `{"type":"integer","minimum":-2147483648,"maximum":2147483647}`

const trackListInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["release_id"],"properties":{"release_id":` + uuidJSONSchema + `}}`
const trackIDInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["track_id"],"properties":{"track_id":` + uuidJSONSchema + `}}`

const trackCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","title"],"properties":{
    "release_id":` + uuidJSONSchema + `,"title":{"type":"string","minLength":1},
    "duration_seconds":` + trackInt32JSONSchema + `,"lyrics":{"type":"string"}
  }
}`

const trackSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["track_id"],"properties":{
    "track_id":` + uuidJSONSchema + `,"track_number":` + trackInt32JSONSchema + `,
    "title":{"type":"string","description":"Empty string preserves the current title."},
    "duration_seconds":` + trackInt32JSONSchema + `,"processing_status":{"type":"string"},"lyrics":{"type":"string"},
    "clear_duration":{"type":"boolean","description":"When true, removes duration even if duration_seconds is supplied."},
    "clear_audio_original":{"type":"boolean","description":"Remove the audio original association; downloads are disabled by the owning service."},
    "clear_lyrics":{"type":"boolean","description":"When true, removes lyrics even if lyrics is supplied."}
  }
}`

const trackCreditJSONSchema = `{
  "type":"object","additionalProperties":false,"properties":{
    "id":` + trackNullableUUIDJSONSchema + `,
    "artist_id":` + trackNullableUUIDJSONSchema + `,"member_id":` + trackNullableUUIDJSONSchema + `,
    "credited_name":{"type":["string","null"]},"credit_role":{"type":["string","null"]},
    "sort_order":` + trackInt32JSONSchema + `
  }
}`
const trackCreditsSnapshotJSONSchema = `{"type":"object","additionalProperties":false,"required":["credits"],"properties":{"credits":{"type":"array","items":` + trackCreditJSONSchema + `}}}`

const trackCreditsSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["track_id","credits","observed"],"properties":{
    "track_id":` + uuidJSONSchema + `,
    "credits":{"type":"array","description":"Desired native credits. Keep existing IDs and omit IDs for new credits. At least one artist_id, member_id, or credited_name is required by the owning service.","items":` + trackCreditJSONSchema + `},
    "observed":` + trackCreditsSnapshotJSONSchema + `
  }
}`

const trackReorderInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["track_ids"],"properties":{
    "track_ids":{"type":"array","minItems":1,"uniqueItems":true,"description":"Every Track ID in exactly one Release, in desired order. The owning service validates complete Release membership.","items":` + uuidJSONSchema + `}
  }
}`

const trackSettingsPropertiesJSONSchema = `
  "track_id":` + uuidJSONSchema + `,"release_id":` + uuidJSONSchema + `,
  "track_number":` + trackInt32JSONSchema + `,"title":{"type":"string"},
  "duration_seconds":{"type":["integer","null"],"minimum":-2147483648,"maximum":2147483647},
  "processing_status":{"type":["string","null"]},"lyrics":{"type":["string","null"]},
  "audio_original_file_id":` + trackNullableUUIDJSONSchema + `
`
const trackSettingsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["track_id","release_id","track_number","title","duration_seconds","processing_status","lyrics","audio_original_file_id"],
  "properties":{` + trackSettingsPropertiesJSONSchema + `}
}`
const trackWithCreditsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["track_id","release_id","track_number","title","duration_seconds","processing_status","lyrics","audio_original_file_id","credits","observed"],
  "properties":{` + trackSettingsPropertiesJSONSchema + `,
    "credits":{"type":"array","items":` + trackCreditJSONSchema + `},"observed":` + trackCreditsSnapshotJSONSchema + `
  }
}`
const trackListOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["tracks"],"properties":{"tracks":{"type":"array","items":` + trackWithCreditsOutputJSONSchema + `}}}`
const trackDeleteOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["success"],"properties":{"success":{"type":"boolean"}}}`
