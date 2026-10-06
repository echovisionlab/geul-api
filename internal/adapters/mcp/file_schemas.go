package mcp

import "encoding/json"

func fileTransferInputSchema() json.RawMessage  { return json.RawMessage(fileTransferInputJSONSchema) }
func fileTransferOutputSchema() json.RawMessage { return json.RawMessage(fileTransferOutputJSONSchema) }
func fileReadInputSchema() json.RawMessage      { return json.RawMessage(fileReadInputJSONSchema) }
func fileReadOutputSchema() json.RawMessage     { return json.RawMessage(fileReadOutputJSONSchema) }

const fileUploadInputJSONSchema = `{
 "type":"object","additionalProperties":false,"required":["file","kind","correlation_id"],
 "properties":{
  "file":{"type":"object","additionalProperties":false,"required":["download_url","file_id"],"properties":{
   "download_url":{"type":"string","format":"uri","pattern":"^https://.+$","minLength":1,"maxLength":4096,"description":"Connector-resolved HTTPS download URL. Never echo or persist this signed source URL."},
   "file_id":{"type":"string","minLength":1,"description":"Opaque ChatGPT attachment ID, not a DSUB File UUID."},
   "file_name":{"type":"string","maxLength":512,"description":"Original filename hint; safe basename and verified MIME extension are retained."},
   "mime_type":{"type":"string","maxLength":255,"description":"Untrusted attachment MIME hint; verified bytes determine stored MIME."}
  }},
  "kind":{"enum":["general","image","video","audio","attachment","mesh"],"description":"Standalone File ingest kind; Track associations use file_transfer."},
  "correlation_id":{"type":"string","format":"uuid","description":"Stable UUID for this import, reused on retries by this Member. Use a new UUID for a different attachment."}
 }
}`

const fileKindJSONSchema = `{"enum":["general","image","video","audio","attachment","mesh","track_audio"],"description":"File ingest kind. track_audio attaches original audio through existing Track authority and CAS; audio remains an independent editor File."}`
const fileMultipartTransportJSONSchema = `{"enum":["browser_upload_page","presigned_multipart"],"description":"Byte transport through the existing authenticated browser upload flow; MCP carries metadata only."}`
const fileSessionHandleJSONSchema = `{
  "oneOf":[{
  "type":"array","description":"Independent File session: [transport, kind, File UUID, upload ID]. Pass all four values unchanged.","prefixItems":[
    ` + fileMultipartTransportJSONSchema + `,
    {"enum":["general","image","video","audio","attachment","mesh"]},
    {"type":"string","format":"uuid","description":"File UUID."},
    {"type":"string","minLength":1,"maxLength":512,"description":"Upload session ID."}
  ],"items":false,"minItems":4,"maxItems":4
  },{
    "type":"array","description":"Track audio session: [transport, track_audio, File UUID, upload ID, Track UUID, expected current audio File UUID or empty string]. Pass all six values unchanged; empty CAS means the Track had no original audio.",
    "prefixItems":[` + fileMultipartTransportJSONSchema + `,{"const":"track_audio"},{"type":"string","format":"uuid"},{"type":"string","minLength":1,"maxLength":512},{"type":"string","format":"uuid"},{"oneOf":[{"const":""},{"type":"string","format":"uuid","minLength":1}]}],
    "items":false,"minItems":6,"maxItems":6
  }]
}`

const fileTrackTargetConditionJSONSchema = `{
  "if":{"properties":{"k":{"const":"track_audio"}},"required":["k"]},
  "then":{"required":["track_id"]},
  "else":{"not":{"anyOf":[{"required":["track_id"]},{"required":["expected_current_file_id"]}]}}
}`

const fileTransferInputJSONSchema = `{
  "type":"object",
  "oneOf":[
    {
      "additionalProperties":false,"required":["a","k","t","n","m","s"],"allOf":[` + fileTrackTargetConditionJSONSchema + `],
      "properties":{
        "a":{"const":"begin","description":"Start one File transfer."},"k":` + fileKindJSONSchema + `,"t":` + fileMultipartTransportJSONSchema + `,
        "n":{"type":"string","minLength":1,"maxLength":512,"description":"Original filename including its extension."},
        "m":{"type":"string","minLength":1,"maxLength":255,"description":"Original MIME type; must match the filename and verified bytes."},
        "s":{"type":"integer","minimum":1,"description":"Original File size in bytes."},
        "lm":{"type":"integer","minimum":0,"description":"Original last-modified time in Unix epoch milliseconds, used for upload resumption."},
        "track_id":{"type":"string","format":"uuid","description":"Required only for track_audio; existing Track UUID."},
        "expected_current_file_id":{"type":"string","format":"uuid","description":"Track audio replacement CAS: copy the current original audio File UUID. Omit only when no original audio is attached."}
      }
    },
    {
      "additionalProperties":false,"required":["a","k","t","u"],
      "allOf":[` + fileTrackTargetConditionJSONSchema + `,{"if":{"properties":{"k":{"enum":["image","video","audio","attachment","mesh","track_audio"]}},"required":["k"]},"then":{"required":["correlation_id"]}}],
      "properties":{
		"a":{"const":"begin"},"k":` + fileKindJSONSchema + `,"t":{"const":"remote_https"},
			"u":{"type":"string","format":"uri","pattern":"^https://.+$","minLength":1,"maxLength":4096,"description":"Public HTTPS source URL without credentials or a fragment. Server import verifies bytes and uses existing media processing."},
        "track_id":{"type":"string","format":"uuid","description":"Required only for track_audio; existing Track UUID."},
        "expected_current_file_id":{"type":"string","format":"uuid","description":"Track audio replacement CAS: current original audio File UUID; omit only when no audio is attached."},
        "correlation_id":{"type":"string","format":"uuid","description":"Stable UUID for one durable remote import. Required except for general File import. Reuse the same UUID on retry; use a new UUID for a different source import."}
      }
    },
    {
      "additionalProperties":false,"required":["a","h"],
      "properties":{"a":{"const":"status"},"h":` + fileSessionHandleJSONSchema + `}
    },
    {
      "additionalProperties":false,"required":["a","h"],
      "properties":{
        "a":{"const":"complete"},"h":` + fileSessionHandleJSONSchema + `,
        "client_media_bundle_id":{"type":"string","format":"uuid","description":"Prepared browser media bundle ID from status. Required by FileService for direct audio/video completion; prepare browser derivatives first."}
      }
    }
  ]
}`

const fileReadInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["f"],
  "properties":{"f":{"type":"string","format":"uuid","description":"Existing File UUID to read with current delivery authorization."}}
}`

const fileReferenceJSONSchema = `{
  "type":"array","description":"Delivery reference tuple: [kind, File/Asset/generation ID, URL, extension, MIME type, size in bytes, filename, expiry]. Empty metadata strings or size 0 mean unavailable for that reference; expiry is null for non-expiring references.","prefixItems":[
    {"enum":["inline","download","asset","playback","thumbnail","spectrogram","waveform"]},
    {"type":"string","description":"File, Asset, or playback generation ID according to kind."},
    {"type":"string","format":"uri","description":"Authorized delivery URL."},
    {"type":"string","description":"Extension without the dot, when available."},
    {"type":"string","description":"MIME type, when available."},
    {"type":"integer","minimum":0,"description":"Reference size in bytes; 0 when unavailable."},
    {"type":"string","description":"Suggested filename, when available."},
    {"type":["string","null"],"format":"date-time","description":"UTC RFC3339 expiry for expiring URLs, otherwise null."}
  ],"items":false,"minItems":8,"maxItems":8
}`

const fileHandleJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["i","n","x","m","z","r"],
  "properties":{
    "i":{"type":"string","format":"uuid","description":"Verified File UUID."},"n":{"type":"string","description":"Stored filename."},
    "x":{"type":"string","minLength":1,"description":"Stored extension without the dot."},"m":{"type":"string","minLength":1,"description":"Verified MIME type."},
    "z":{"type":"integer","minimum":1,"description":"Stored File size in bytes."},"d":{"type":"integer","minimum":0,"description":"Media duration in seconds, when available."},
    "s":{"enum":["processing","ready","failed"],"description":"Derivative processing state, when applicable."},"p":{"type":"integer","minimum":0,"maximum":100,"description":"Derivative processing progress in percent."},
    "r":{"type":"array","maxItems":7,"description":"Available authorized delivery and derivative references.","items":` + fileReferenceJSONSchema + `}
  }
}`

const fileSessionJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["h","s","p","c","u"],
  "properties":{
    "h":` + fileSessionHandleJSONSchema + `,
    "s":{"enum":["initiated","uploading","finalizing"],"description":"Current upload session state."},
    "n":{"type":"string","description":"Original filename."},"m":{"type":"string","description":"Requested MIME type."},"z":{"type":"integer","minimum":1,"description":"Original File size in bytes."},
    "p":{"type":"integer","minimum":1,"description":"Total number of upload parts."},"c":{"type":"integer","minimum":1,"description":"Upload part chunk size in bytes."},
    "u":{"type":"array","description":"Uploaded part numbers, starting at 1.","items":{"type":"integer","minimum":1}},
    "a":{"type":"string","format":"date-time","description":"Last activity time in UTC RFC3339."},
    "client_media_bundle_id":{"type":"string","format":"uuid","description":"Current browser-prepared media bundle ID; copy to complete for direct audio/video after browser preparation."}
  }
}`

const fileTransferOutputJSONSchema = `{
  "type":"object","oneOf":[
    {
      "additionalProperties":false,"required":["s","x"],
      "properties":{"s":{"enum":["initiated","uploading","finalizing"],"description":"Current transfer state."},"x":{"$ref":"#/$defs/session","description":"Active upload session and progress."}}
    },
    {
      "additionalProperties":false,"required":["s","f"],
      "properties":{"s":{"const":"ready","description":"Verified original File is stored; derivatives may still be processing."},"f":{"$ref":"#/$defs/file","description":"Verified File metadata and current delivery references."}}
    }
  ],
  "$defs":{"session":` + fileSessionJSONSchema + `,"file":` + fileHandleJSONSchema + `}
}`

const fileReadOutputJSONSchema = fileHandleJSONSchema
