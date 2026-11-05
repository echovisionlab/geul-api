package mcp

import "encoding/json"

func translationTargetInputSchema() json.RawMessage {
	return json.RawMessage(translationTargetInputJSONSchema)
}
func translationGetInputSchema() json.RawMessage {
	return json.RawMessage(translationGetInputJSONSchema)
}
func translationXLIFFExportInputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFExportInputJSONSchema)
}
func translationXLIFFImportInputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFImportInputJSONSchema)
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

const localeJSONSchema = `{"type":"string","description":"Exact supported locale code; aliases and case variants are not normalized.","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}`
const translationProfileJSONSchema = `{"description":"Document profile, paired with the document reference d.","allOf":[` + domainJSONSchema + `]}`

const translationTargetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d"],
  "properties":{"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Exact owning document reference, also used as d by document_read.","minLength":1,"maxLength":256}}
}`

const translationGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Exact owning document reference, also used as d by document_read.","minLength":1,"maxLength":256},"l":` + localeJSONSchema + `}
}`

const translationXLIFFExportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","m"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":` + localeJSONSchema + `,
    "m":{"description":"patch exports the explicit u selection; replace exports the complete current unit manifest.","enum":["patch","replace"]},
    "u":{"type":"array","description":"Opaque unit IDs from an exported XLIFF manifest. Required and non-empty for patch; omitted or empty for replace.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  }
}`

const translationXLIFFImportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","m","f"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference matching the uploaded XLIFF file identity.","minLength":1,"maxLength":256},
    "l":` + localeJSONSchema + `,
    "m":{"description":"patch updates the explicit uploaded units and preserves other target values; replace requires the complete current unit manifest.","enum":["patch","replace"]},
    "f":{"type":"string","description":"Completed existing upload File ID containing the edited XLIFF. Upload edited XML first; never pass XML or a URL here.","format":"uuid"},
    "er":{"type":"string","description":"Exact target revision r returned by translation_xliff_export. Required for an existing target; omit only to create a currently absent target. A concurrent change causes a conflict.","minLength":1,"maxLength":256}
  }
}`

const translationListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["s","e"],
  "properties":{
    "s":{"description":"Current source locale.","allOf":[` + localeJSONSchema + `]},
    "e":{"type":"array","description":"Existing locale metadata as [locale code, updated_at or null] tuples; no translated content.","items":{"type":"array","prefixItems":[` + localeJSONSchema + `,{"type":["string","null"],"description":"Locale update timestamp when available.","format":"date-time"}],"items":false,"minItems":2,"maxItems":2}}
  }
}`

const translationGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Owning document reference."},"l":{"description":"Locale represented by this existing entry.","allOf":[` + localeJSONSchema + `]},"u":{"type":"string","description":"Locale update timestamp, when available.","format":"date-time"}}
}`

const translationXLIFFExportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["a","s","l","m"],
  "properties":{
    "a":{"type":"object","description":"Exported XLIFF artifact download reference.","additionalProperties":false,"required":["f","u","e","x","t"],"properties":{
      "f":{"type":"string","description":"Generated artifact File ID."},"u":{"type":"string","description":"Signed download URL; retrieve the XML before expiration.","format":"uri"},"e":{"type":"string","description":"Download URL expiration timestamp.","format":"date-time"},
      "x":{"type":"string","description":"File extension.","minLength":1,"maxLength":32},"t":{"type":"string","description":"MIME type.","minLength":1,"maxLength":128},"n":{"type":"string","description":"Suggested file name.","minLength":1,"maxLength":512}
    }},
    "s":{"description":"Source locale.","allOf":[` + localeJSONSchema + `]},"l":{"description":"Target locale.","allOf":[` + localeJSONSchema + `]},"m":{"description":"Applied export mode.","enum":["patch","replace"]},"r":{"type":"string","description":"Current opaque target revision. Pass unchanged as import er. Omitted when the target does not exist.","minLength":1,"maxLength":256}
  }
}`

const translationXLIFFImportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["r","c","u"],
  "properties":{"r":{"type":"string","description":"Target revision after import.","minLength":1,"maxLength":256},"c":{"type":"boolean","description":"Whether stored target values changed."},"u":{"type":"array","description":"Opaque unit IDs affected by the validated import.","uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}}
}`
