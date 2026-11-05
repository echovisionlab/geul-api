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

func translationUnitsGetInputSchema() json.RawMessage {
	return json.RawMessage(translationUnitsGetInputJSONSchema)
}
func translationUnitsGetOutputSchema() json.RawMessage {
	return json.RawMessage(translationUnitsGetOutputJSONSchema)
}
func translationUnitsPutInputSchema() json.RawMessage {
	return json.RawMessage(translationUnitsPutInputJSONSchema)
}

// translationUnitMarkupJSON describes unit markup, already escaped for a JSON
// string.
const translationUnitMarkupJSON = `XLIFF 2.2 inline markup: <pc id=\"…\">…</pc> wraps formatted or linked text, and <ph id=\"…\" equiv=\"…\"/> stands for a placeholder whose equiv shows what it renders. Text escapes &, < and >.`

const translationUnitsGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":{"description":"Target locale; it must differ from the source locale.","allOf":[` + localeJSONSchema + `]},
    "u":{"type":"array","description":"Unit IDs to read. Omit to read every current unit.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  }
}`

const translationUnitsGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["s","l","u"],
  "properties":{
    "s":{"description":"Source locale.","allOf":[` + localeJSONSchema + `]},
    "l":{"description":"Target locale.","allOf":[` + localeJSONSchema + `]},
    "r":{"type":"string","description":"Current opaque target revision. Pass unchanged as translation_units_put er. Omitted when the target does not exist.","minLength":1,"maxLength":256},
    "u":{"type":"array","description":"Units in document order.","items":{"type":"object","additionalProperties":false,"required":["i","g","s"],"properties":{
      "i":{"type":"string","description":"Unit ID; the key for translation_units_put."},
      "g":{"type":"string","description":"Group the unit belongs to, such as one block or the document metadata."},
      "c":{"type":"string","description":"Context note for the unit."},
      "s":{"type":"string","description":"Source text as ` + translationUnitMarkupJSON + `"},
      "t":{"type":"string","description":"Current target in the same markup. Omitted when the unit has no target yet."}
    }}}
  }
}`

const translationUnitsPutInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","u"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":{"description":"Target locale; it must differ from the source locale.","allOf":[` + localeJSONSchema + `]},
    "u":{"type":"object","description":"Target per unit ID from translation_units_get, as ` + translationUnitMarkupJSON + ` Keep every source code with the same id and nesting, and the same line breaks and {{…}} placeholders.","minProperties":1,"maxProperties":1000,"additionalProperties":{"type":"string","maxLength":100000}},
    "er":{"type":"string","description":"Exact target revision r returned by translation_units_get. Required for an existing target; omit only to create a currently absent target. A concurrent change causes a conflict.","minLength":1,"maxLength":256}
  }
}`
