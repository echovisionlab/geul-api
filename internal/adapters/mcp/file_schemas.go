package mcp

import "encoding/json"

func fileTransferInputSchema() json.RawMessage  { return json.RawMessage(fileTransferInputJSONSchema) }
func fileTransferOutputSchema() json.RawMessage { return json.RawMessage(fileTransferOutputJSONSchema) }
func fileReadInputSchema() json.RawMessage      { return json.RawMessage(fileReadInputJSONSchema) }
func fileReadOutputSchema() json.RawMessage     { return json.RawMessage(fileReadOutputJSONSchema) }

const fileKindJSONSchema = `{"enum":["general","image","video","audio","attachment","mesh"]}`
const fileMultipartTransportJSONSchema = `{"enum":["browser_upload_page","presigned_multipart"]}`
const fileSessionHandleJSONSchema = `{
  "type":"array","prefixItems":[
    ` + fileMultipartTransportJSONSchema + `,
    ` + fileKindJSONSchema + `,
    {"type":"string","format":"uuid"},
    {"type":"string","minLength":1,"maxLength":512}
  ],"items":false,"minItems":4,"maxItems":4
}`

const fileTransferInputJSONSchema = `{
  "type":"object",
  "oneOf":[
    {
      "additionalProperties":false,"required":["a","k","t","n","m","s"],
      "properties":{
        "a":{"const":"begin"},"k":` + fileKindJSONSchema + `,"t":` + fileMultipartTransportJSONSchema + `,
        "n":{"type":"string","minLength":1,"maxLength":512},
        "m":{"type":"string","minLength":1,"maxLength":255},
        "s":{"type":"integer","minimum":1},"lm":{"type":"integer","minimum":0}
      }
    },
    {
      "additionalProperties":false,"required":["a","k","t","u"],
      "properties":{
		"a":{"const":"begin"},"k":` + fileKindJSONSchema + `,"t":{"const":"remote_https"},
		"u":{"type":"string","format":"uri","pattern":"^https://","minLength":1,"maxLength":4096}
      }
    },
    {
      "additionalProperties":false,"required":["a","h"],
      "properties":{"a":{"const":"status"},"h":` + fileSessionHandleJSONSchema + `}
    },
    {
      "additionalProperties":false,"required":["a","h"],
      "properties":{"a":{"const":"complete"},"h":` + fileSessionHandleJSONSchema + `}
    }
  ]
}`

const fileReadInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["f"],
  "properties":{"f":{"type":"string","format":"uuid"}}
}`

const fileReferenceJSONSchema = `{
  "type":"array","prefixItems":[
    {"enum":["inline","download","asset","playback","thumbnail","spectrogram","waveform"]},
    {"type":"string"},{"type":"string","format":"uri"},{"type":"string"},
    {"type":"string"},{"type":"integer","minimum":0},{"type":"string"},
    {"type":["string","null"],"format":"date-time"}
  ],"items":false,"minItems":8,"maxItems":8
}`

const fileHandleJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["i","n","x","m","z","r"],
  "properties":{
    "i":{"type":"string","format":"uuid"},"n":{"type":"string"},
    "x":{"type":"string","minLength":1},"m":{"type":"string","minLength":1},
    "z":{"type":"integer","minimum":1},"d":{"type":"integer","minimum":0},
    "s":{"enum":["processing","ready","failed"]},"p":{"type":"integer","minimum":0,"maximum":100},
    "r":{"type":"array","maxItems":7,"items":` + fileReferenceJSONSchema + `}
  }
}`

const fileSessionJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["h","s","p","c","u"],
  "properties":{
    "h":` + fileSessionHandleJSONSchema + `,
    "s":{"enum":["initiated","uploading","finalizing"]},
    "n":{"type":"string"},"m":{"type":"string"},"z":{"type":"integer","minimum":1},
    "p":{"type":"integer","minimum":1},"c":{"type":"integer","minimum":1},
    "u":{"type":"array","items":{"type":"integer","minimum":1}},
    "a":{"type":"string","format":"date-time"}
  }
}`

const fileTransferOutputJSONSchema = `{
  "type":"object","oneOf":[
    {
      "additionalProperties":false,"required":["s","x"],
      "properties":{"s":{"enum":["initiated","uploading","finalizing"]},"x":{"$ref":"#/$defs/session"}}
    },
    {
      "additionalProperties":false,"required":["s","f"],
      "properties":{"s":{"const":"ready"},"f":{"$ref":"#/$defs/file"}}
    }
  ],
  "$defs":{"session":` + fileSessionJSONSchema + `,"file":` + fileHandleJSONSchema + `}
}`

const fileReadOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["i","n","x","m","z","r"],
  "properties":{
    "i":{"type":"string","format":"uuid"},"n":{"type":"string"},
    "x":{"type":"string","minLength":1},"m":{"type":"string","minLength":1},
    "z":{"type":"integer","minimum":1},"d":{"type":"integer","minimum":0},
    "s":{"enum":["processing","ready","failed"]},"p":{"type":"integer","minimum":0,"maximum":100},
    "r":{"type":"array","maxItems":7,"items":` + fileReferenceJSONSchema + `}
  }
}`
