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
   "file_id":{"type":"string","minLength":1,"description":"Opaque ChatGPT attachment ID, not a rawsector File UUID."},
   "file_name":{"type":"string","maxLength":512,"description":"Original filename hint; safe basename and verified MIME extension are retained."},
   "mime_type":{"type":"string","maxLength":255,"description":"Untrusted attachment MIME hint; verified bytes determine stored MIME."}
  }},
  "kind":{"enum":["general","image","video","audio","attachment","mesh"],"description":"Standalone File ingest kind; Track associations use file_transfer."},
  "correlation_id":{"type":"string","format":"uuid","description":"Stable UUID for this import, reused on retries by this Member. Use a new UUID for a different attachment."}
 }
}`

const fileMultipartTransportJSONSchema = `{"enum":["browser_upload_page","presigned_multipart"],"description":"Byte transport through the existing authenticated browser upload flow; MCP carries metadata only."}`
const fileSessionHandleJSONSchema = `{"oneOf":` + fileSessionHandleVariantsJSONSchema + `}`
const fileSessionHandleVariantsJSONSchema = `[{
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
  }]`

// fileTransferInputJSONSchema is one flat root object so function-calling
// clients can render its arguments; per-action and per-transport field rules
// are enforced by FileTools.transfer and the MCP File facade.
const fileTransferInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["a"],
  "properties":{
    "a":{"enum":["begin","status","parts","complete","abort"],"description":"Action. begin starts one File transfer (requires k and t); status inspects, parts re-issues upload URLs for missing parts, complete finishes and abort discards a multipart session (all require h). Send only the fields listed for the chosen action/transport."},
    "k":{"enum":["general","image","video","audio","attachment","mesh","track_audio"],"description":"begin only, required. File ingest kind. track_audio attaches original audio through existing Track authority and CAS; audio remains an independent editor File."},
    "t":{"enum":["browser_upload_page","presigned_multipart","remote_https"],"description":"begin only, required. presigned_multipart: returns URLs you PUT the bytes to yourself. browser_upload_page: the authenticated browser upload flow sends the bytes. remote_https: server import from a public HTTPS URL."},
    "n":{"type":"string","minLength":1,"maxLength":512,"description":"begin multipart: required; forbidden for remote_https. Original filename including its extension."},
    "m":{"type":"string","minLength":1,"maxLength":255,"description":"begin multipart: required; forbidden for remote_https. Original MIME type; must match the filename and verified bytes."},
    "s":{"type":"integer","minimum":1,"description":"begin multipart: required; forbidden for remote_https. Original File size in bytes."},
    "lm":{"type":"integer","minimum":0,"description":"begin multipart: optional; forbidden for remote_https. Original last-modified time in Unix epoch milliseconds, used for upload resumption."},
    "u":{"type":"string","format":"uri","pattern":"^https://.+$","minLength":1,"maxLength":4096,"description":"begin remote_https: required; forbidden for multipart. Public HTTPS source URL without credentials or a fragment. Server import verifies bytes and uses existing media processing."},
    "track_id":{"type":"string","format":"uuid","description":"begin: required when k is track_audio, forbidden otherwise. Existing Track UUID."},
    "expected_current_file_id":{"type":"string","format":"uuid","description":"begin, k=track_audio only: Track audio replacement CAS; copy the current original audio File UUID. Omit only when no original audio is attached. Forbidden for other kinds."},
    "correlation_id":{"type":"string","format":"uuid","description":"begin remote_https only: stable UUID for one durable remote import; required for every kind except general. Reuse the same UUID on retry; use a new UUID for a different source import. Forbidden for multipart."},
    "h":{"description":"status/complete: required; forbidden for begin. Session handle returned by begin or status; pass it unchanged.","oneOf":` + fileSessionHandleVariantsJSONSchema + `},
    "client_media_bundle_id":{"type":"string","format":"uuid","description":"complete only, optional. Prepared browser media bundle ID from status. Required by FileService for direct audio/video completion; prepare browser derivatives first."}
  }
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
    "client_media_bundle_id":{"type":"string","format":"uuid","description":"Current browser-prepared media bundle ID; copy to complete for direct audio/video after browser preparation."},
    "l":{"type":"array","description":"Upload URLs for presigned_multipart: PUT each part's exact bytes, then call complete.","items":{"type":"array","prefixItems":[{"type":"integer","minimum":1,"description":"Part number."},{"type":"string","format":"uri","description":"Short-lived PUT URL."},{"type":"integer","minimum":1,"description":"Exact part size in bytes."}],"items":false,"minItems":3,"maxItems":3}},
    "e":{"type":"string","format":"date-time","description":"When the URLs in l expire."}
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
    },
    {
      "additionalProperties":false,"required":["s"],
      "properties":{"s":{"const":"aborted","description":"The session and its uploaded parts were discarded."}}
    }
  ],
  "$defs":{"session":` + fileSessionJSONSchema + `,"file":` + fileHandleJSONSchema + `}
}`

const fileReadOutputJSONSchema = fileHandleJSONSchema
