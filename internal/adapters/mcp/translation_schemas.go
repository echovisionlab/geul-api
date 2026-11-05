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
  "type":"object","additionalProperties":false,"required":["p","d"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":{"description":"One target locale, read whole; it must differ from the source locale. Omit l and ls to read every existing translation.","allOf":[` + localeJSONSchema + `]},
    "ls":{"type":"array","description":"Several target locales to read together. Needs b, q or u.","minItems":1,"maxItems":64,"uniqueItems":true,"items":` + localeJSONSchema + `},
    "u":{"type":"array","description":"Unit IDs to read. Omit to read every current unit.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}},
    "b":{"type":"array","description":"Read only the units of these Block or section handles, from document_read.","maxItems":256,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":160}},
    "q":{"type":"string","description":"Read only units whose text in the source or a requested locale contains this, ignoring case and markup.","minLength":1,"maxLength":200}
  }
}`

const translationUnitsGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["s","u"],
  "properties":{
    "s":{"description":"Source locale.","allOf":[` + localeJSONSchema + `]},
    "l":{"description":"Target locale of a one-locale read.","allOf":[` + localeJSONSchema + `]},
    "r":{"description":"One-locale read: the current opaque target revision, omitted when the target does not exist. Several locales: the revision per locale.","type":["string","object"],"additionalProperties":{"type":"string"}},
    "u":{"type":"array","description":"Units in document order.","items":{"type":"object","additionalProperties":false,"required":["i","g","s"],"properties":{
      "i":{"type":"string","description":"Unit ID."},
      "g":{"type":"string","description":"Group the unit belongs to, such as one block or the document metadata."},
      "c":{"type":"string","description":"Context note for the unit."},
      "s":{"type":"string","description":"Source text as ` + translationUnitMarkupJSON + ` Plain text for a one-run unit in a several-locale read."},
      "p":{"type":"string","description":"Plain source text of a marked-up unit, to pass as translation_units_put e[].s."},
      "t":{"description":"One-locale read: the current target, omitted when absent. Several locales: the target per locale.","type":["string","object"],"additionalProperties":{"type":"string"}}
    }}}
  }
}`

const translationUnitsPutInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "e":{"type":"array","description":"Edits applied across locales in one transaction. Do not combine with l, u or er.","minItems":1,"maxItems":1000,"items":{"type":"object","additionalProperties":false,"required":["s","t"],"properties":{
      "s":{"type":"string","description":"The unit's current plain source text, without markup."},
      "i":{"type":"string","description":"Unit ID, when several units share s.","minLength":1,"maxLength":256},
      "b":{"type":"string","description":"Block or section handle, when several units share s.","minLength":1,"maxLength":160},
      "t":{"type":"object","description":"Target per locale. Plain text for a one-run unit; otherwise ` + translationUnitMarkupJSON + `","minProperties":1,"maxProperties":64,"additionalProperties":{"type":"string","maxLength":100000}}
    }}},
    "l":{"description":"With u: the one target locale; it must differ from the source locale.","allOf":[` + localeJSONSchema + `]},
    "u":{"type":"object","description":"With l: target per unit ID from translation_units_get, as ` + translationUnitMarkupJSON + ` Keep every source code with the same id and nesting, and the same line breaks and {{…}} placeholders.","minProperties":1,"maxProperties":1000,"additionalProperties":{"type":"string","maxLength":100000}},
    "er":{"type":"string","description":"With l and u: exact target revision r returned by translation_units_get. Required for an existing target; omit only to create a currently absent target. A concurrent change causes a conflict.","minLength":1,"maxLength":256}
  }
}`

func translationUnitsPutOutputSchema() json.RawMessage {
	return json.RawMessage(translationUnitsPutOutputJSONSchema)
}

const translationUnitsPutOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["r","c"],
  "properties":{
    "r":{"description":"With l and u: the target revision after the write. With e: the revision per locale.","type":["string","object"],"additionalProperties":{"type":"string"}},
    "c":{"description":"With l and u: whether stored target values changed. With e: whether they changed per locale.","type":["boolean","object"],"additionalProperties":{"type":"boolean"}},
    "u":{"type":"array","description":"With l and u: opaque unit IDs affected by the write.","uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  }
}`
