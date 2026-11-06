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
const fileSessionHandleJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["transport","kind","file_id","upload_id"],"description":"Upload session returned by begin or status; pass it back unchanged.",
  "properties":{
    "transport":` + fileMultipartTransportJSONSchema + `,
    "kind":{"enum":["general","image","video","audio","attachment","mesh","track_audio"]},
    "file_id":{"type":"string","format":"uuid"},
    "upload_id":{"type":"string","minLength":1,"maxLength":512},
    "track_id":{"type":"string","format":"uuid","description":"Track audio only."},
    "expected_current_file_id":{"type":"string","format":"uuid","description":"Track audio only: the current original audio File UUID, left out when the Track had none."}
  }
}`

// fileTransferInputJSONSchema is one flat root object so function-calling
// clients can render its arguments; per-action and per-transport field rules
// are enforced by FileTools.transfer and the MCP File facade.
const fileTransferInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"enum":["begin","status","parts","complete","abort"],"description":"begin starts one File transfer (requires kind and transport); status inspects, parts re-issues upload URLs for missing parts, complete finishes and abort discards a multipart session (all require session). Send only the fields listed for the chosen action/transport."},
    "kind":{"enum":["general","image","video","audio","attachment","mesh","track_audio"],"description":"begin only, required. File ingest kind. track_audio attaches original audio through existing Track authority and CAS; audio remains an independent editor File."},
    "transport":{"enum":["browser_upload_page","presigned_multipart","remote_https"],"description":"begin only, required. presigned_multipart: returns URLs you PUT the bytes to yourself. browser_upload_page: the authenticated browser upload flow sends the bytes. remote_https: server import from a public HTTPS URL."},
    "file_name":{"type":"string","minLength":1,"maxLength":512,"description":"begin multipart: required; forbidden for remote_https. Original filename including its extension."},
    "mime_type":{"type":"string","minLength":1,"maxLength":255,"description":"begin multipart: required; forbidden for remote_https. Original MIME type; must match the filename and verified bytes."},
    "file_size":{"type":"integer","minimum":1,"description":"begin multipart: required; forbidden for remote_https. Original File size in bytes."},
    "last_modified_ms":{"type":"integer","minimum":0,"description":"begin multipart: optional; forbidden for remote_https. Original last-modified time in Unix epoch milliseconds, used for upload resumption."},
    "remote_url":{"type":"string","format":"uri","pattern":"^https://.+$","minLength":1,"maxLength":4096,"description":"begin remote_https: required; forbidden for multipart. Public HTTPS source URL without credentials or a fragment. Server import verifies bytes and uses existing media processing."},
    "track_id":{"type":"string","format":"uuid","description":"begin: required when kind is track_audio, forbidden otherwise. Existing Track UUID."},
    "expected_current_file_id":{"type":"string","format":"uuid","description":"begin, kind=track_audio only: Track audio replacement CAS; copy the current original audio File UUID. Omit only when no original audio is attached. Forbidden for other kinds."},
    "correlation_id":{"type":"string","format":"uuid","description":"begin remote_https only: stable UUID for one durable remote import; required for every kind except general. Reuse the same UUID on retry; use a new UUID for a different source import. Forbidden for multipart."},
    "session":` + fileSessionHandleJSONSchema + `,
    "client_media_bundle_id":{"type":"string","format":"uuid","description":"complete only, optional. Prepared browser media bundle ID from status. Required by FileService for direct audio/video completion; prepare browser derivatives first."}
  }
}`

const fileReadInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["file_id"],
  "properties":{"file_id":{"type":"string","format":"uuid","description":"Existing File UUID to read with current delivery authorization."}}
}`

const fileReferenceJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["kind","id","url"],"description":"One authorized delivery or derivative reference.","properties":{
    "kind":{"enum":["inline","download","asset","playback","thumbnail","spectrogram","waveform"]},
    "id":{"type":"string","description":"File, Asset, or playback generation ID according to kind."},
    "url":{"type":"string","format":"uri","description":"Authorized delivery URL."},
    "extension":{"type":"string","description":"Extension without the dot, when available."},
    "mime_type":{"type":"string","description":"MIME type, when available."},
    "file_size":{"type":"integer","minimum":1,"description":"Reference size in bytes, when available."},
    "file_name":{"type":"string","description":"Suggested filename, when available."},
    "expires_at":{"type":"string","format":"date-time","description":"UTC RFC3339 expiry for expiring URLs; left out otherwise."}
  }
}`

const fileHandleJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["file_id","file_name","extension","mime_type","file_size","references"],
  "properties":{
    "file_id":{"type":"string","format":"uuid","description":"Verified File UUID."},"file_name":{"type":"string","description":"Stored filename."},
    "extension":{"type":"string","minLength":1,"description":"Stored extension without the dot."},"mime_type":{"type":"string","minLength":1,"description":"Verified MIME type."},
    "file_size":{"type":"integer","minimum":1,"description":"Stored File size in bytes."},"duration_seconds":{"type":"integer","minimum":0,"description":"Media duration in seconds, when available."},
    "derivative_status":{"enum":["processing","ready","failed"],"description":"Derivative processing state, when applicable."},"derivative_percentage":{"type":"integer","minimum":0,"maximum":100,"description":"Derivative processing progress in percent."},
    "references":{"type":"array","maxItems":7,"description":"Available authorized delivery and derivative references.","items":` + fileReferenceJSONSchema + `}
  }
}`

const fileSessionJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["session","state","total_parts","chunk_size","uploaded_parts"],
  "properties":{
    "session":` + fileSessionHandleJSONSchema + `,
    "state":{"enum":["initiated","uploading","finalizing"],"description":"Current upload session state."},
    "file_name":{"type":"string","description":"Original filename."},"mime_type":{"type":"string","description":"Requested MIME type."},"file_size":{"type":"integer","minimum":1,"description":"Original File size in bytes."},
    "total_parts":{"type":"integer","minimum":1,"description":"Total number of upload parts."},"chunk_size":{"type":"integer","minimum":1,"description":"Upload part chunk size in bytes."},
    "uploaded_parts":{"type":"array","description":"Uploaded part numbers, starting at 1.","items":{"type":"integer","minimum":1}},
    "last_activity_at":{"type":"string","format":"date-time","description":"Last activity time in UTC RFC3339."},
    "client_media_bundle_id":{"type":"string","format":"uuid","description":"Current browser-prepared media bundle ID; copy to complete for direct audio/video after browser preparation."},
    "part_uploads":{"type":"array","description":"Upload URLs for presigned_multipart: PUT each part's exact bytes, then call complete.","items":{"type":"object","additionalProperties":false,"required":["part_number","url","size"],"properties":{"part_number":{"type":"integer","minimum":1},"url":{"type":"string","format":"uri","description":"Short-lived PUT URL."},"size":{"type":"integer","minimum":1,"description":"Exact part size in bytes."}}}},
    "part_uploads_expire_at":{"type":"string","format":"date-time","description":"When the URLs in part_uploads expire."}
  }
}`

const fileTransferOutputJSONSchema = `{
  "type":"object","oneOf":[
    {
      "additionalProperties":false,"required":["state","upload"],
      "properties":{"state":{"enum":["initiated","uploading","finalizing"],"description":"Current transfer state."},"upload":{"$ref":"#/$defs/upload","description":"Active upload session and progress."}}
    },
    {
      "additionalProperties":false,"required":["state","file"],
      "properties":{"state":{"const":"ready","description":"Verified original File is stored; derivatives may still be processing."},"file":{"$ref":"#/$defs/file","description":"Verified File metadata and current delivery references."}}
    },
    {
      "additionalProperties":false,"required":["state"],
      "properties":{"state":{"const":"aborted","description":"The session and its uploaded parts were discarded."}}
    }
  ],
  "$defs":{"upload":` + fileSessionJSONSchema + `,"file":` + fileHandleJSONSchema + `}
}`

const fileReadOutputJSONSchema = fileHandleJSONSchema
