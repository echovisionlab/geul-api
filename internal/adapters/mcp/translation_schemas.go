package mcp

import "encoding/json"

func translationTargetInputSchema() json.RawMessage {
	return json.RawMessage(translationTargetInputJSONSchema)
}
func translationGetInputSchema() json.RawMessage {
	return json.RawMessage(translationGetInputJSONSchema)
}
func translationJobInputSchema() json.RawMessage {
	return json.RawMessage(translationJobInputJSONSchema)
}
func translationJobsListInputSchema() json.RawMessage {
	return json.RawMessage(translationJobsListInputJSONSchema)
}
func translationRegenerateInputSchema() json.RawMessage {
	return json.RawMessage(translationRegenerateInputJSONSchema)
}
func translationXLIFFExportInputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFExportInputJSONSchema)
}
func translationXLIFFImportInputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFImportInputJSONSchema)
}
func translationJobCancellationOutputSchema() json.RawMessage {
	return json.RawMessage(translationJobCancellationOutputJSONSchema)
}
func translationJobsOutputSchema() json.RawMessage {
	return json.RawMessage(translationJobsOutputJSONSchema)
}
func translationListOutputSchema() json.RawMessage {
	return json.RawMessage(translationListOutputJSONSchema)
}
func translationGetOutputSchema() json.RawMessage {
	return json.RawMessage(translationGetOutputJSONSchema)
}
func translationXLIFFExportOutputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFExportOutputJSONSchema)
}
func translationXLIFFImportOutputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFImportOutputJSONSchema)
}

const localeJSONSchema = `{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}`

const translationTargetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d"],
  "properties":{"p":` + domainJSONSchema + `,"d":{"type":"string","minLength":1,"maxLength":256}}
}`

const translationGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{"p":` + domainJSONSchema + `,"d":{"type":"string","minLength":1,"maxLength":256},"l":` + localeJSONSchema + `}
}`

const translationJobInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{"j":{"type":"string","format":"uuid"}}
}`

const translationJobsListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":{"type":"string","minLength":1,"maxLength":256},
    "tl":` + localeJSONSchema + `,
    "sl":` + localeJSONSchema + `,
    "s":{"type":"array","maxItems":2,"uniqueItems":true,"items":{"enum":["queued","running"]}},
    "n":{"type":"integer","minimum":0,"maximum":100},
    "o":{"type":"integer","minimum":0},
    "k":{"enum":["requested_at","updated_at","target_locale","status"]},
    "z":{"type":"boolean"}
  },
  "dependentRequired":{"p":["d"],"d":["p"],"z":["k"]}
}`

const translationRegenerateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":{"type":"string","minLength":1,"maxLength":256},
    "l":{"type":"array","minItems":1,"maxItems":32,"uniqueItems":true,"items":` + localeJSONSchema + `}
  }
}`

const translationXLIFFExportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","m"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":{"type":"string","minLength":1,"maxLength":256},
    "l":` + localeJSONSchema + `,
    "m":{"enum":["patch","replace"]},
    "u":{"type":"array","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  },
  "allOf":[
    {"if":{"properties":{"m":{"const":"patch"}},"required":["m"]},"then":{"required":["u"],"properties":{"u":{"minItems":1}}}},
    {"if":{"properties":{"m":{"const":"replace"}},"required":["m"]},"then":{"properties":{"u":{"maxItems":0}}}}
  ]
}`

const translationXLIFFImportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","m","f"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":{"type":"string","minLength":1,"maxLength":256},
    "l":` + localeJSONSchema + `,
    "m":{"enum":["patch","replace"]},
    "f":{"type":"string","format":"uuid"},
    "er":{"type":"string","minLength":1,"maxLength":256}
  }
}`

const compactJobDefinitionJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["i","p","d","tl","sl","s","o"],
  "properties":{
    "i":{"type":"string","format":"uuid"},"p":` + domainJSONSchema + `,"d":{"type":"string","minLength":1,"maxLength":256},
    "tl":` + localeJSONSchema + `,"sl":` + localeJSONSchema + `,
    "s":{"enum":["queued","running"]},
    "o":{"type":"string","format":"uuid"},
    "rq":{"type":"string","format":"date-time"},"st":{"type":"string","format":"date-time"}
  }
}`

const translationJobCancellationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{"j":{"type":"string","format":"uuid"}}
}`

const translationJobsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{
    "j":{"type":"array","items":{"$ref":"#/$defs/job"}},
    "g":{"type":"array","prefixItems":[{"type":"integer"},{"type":"integer"},{"type":"integer"},{"type":"boolean"}],"items":false,"minItems":4,"maxItems":4}
  },
  "$defs":{"job":` + compactJobDefinitionJSONSchema + `}
}`

const translationListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["s","e"],
  "properties":{
    "s":` + localeJSONSchema + `,
    "e":{"type":"array","items":{"type":"array","prefixItems":[` + localeJSONSchema + `,{"type":["string","null"],"format":"date-time"}],"items":false,"minItems":2,"maxItems":2}}
  }
}`

const translationGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{"p":` + domainJSONSchema + `,"d":{"type":"string"},"l":` + localeJSONSchema + `,"u":{"type":"string","format":"date-time"}}
}`

const translationXLIFFExportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["a","s","l","m"],
  "properties":{
    "a":{"type":"object","additionalProperties":false,"required":["f","u","e","x","t"],"properties":{
      "f":{"type":"string"},"u":{"type":"string","format":"uri"},"e":{"type":"string","format":"date-time"},
      "x":{"type":"string","minLength":1,"maxLength":32},"t":{"type":"string","minLength":1,"maxLength":128},"n":{"type":"string","minLength":1,"maxLength":512}
    }},
    "s":` + localeJSONSchema + `,"l":` + localeJSONSchema + `,"m":{"enum":["patch","replace"]},"r":{"type":"string","minLength":1,"maxLength":256}
  }
}`

const translationXLIFFImportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["r","c","u"],
  "properties":{"r":{"type":"string","minLength":1,"maxLength":256},"c":{"type":"boolean"},"u":{"type":"array","uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}}
}`
