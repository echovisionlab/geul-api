package mcp

import "encoding/json"

func translationTargetInputSchema() json.RawMessage {
	return json.RawMessage(translationTargetInputJSONSchema)
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
func translationXLIFFExportOutputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFExportOutputJSONSchema)
}
func translationXLIFFImportOutputSchema() json.RawMessage {
	return json.RawMessage(translationXLIFFImportOutputJSONSchema)
}

const localeJSONSchema = `{"type":"string","description":"Exact supported locale code; aliases and case variants are not normalized.","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}`
const translationProfileJSONSchema = `{"description":"The type of document_id.","allOf":[` + domainJSONSchema + `]}`

const translationDocumentIDJSONSchema = `{"type":"string","description":"The document_id from document_list or another listing tool.","minLength":1,"maxLength":256}`

const translationTargetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id"],
  "properties":{"document_type":` + translationProfileJSONSchema + `,"document_id":` + translationDocumentIDJSONSchema + `}
}`

const translationXLIFFExportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","locale","mode"],
  "properties":{
    "document_type":` + translationProfileJSONSchema + `,
    "document_id":` + translationDocumentIDJSONSchema + `,
    "locale":` + localeJSONSchema + `,
    "mode":{"description":"patch exports the explicit unit_ids selection; replace exports the complete current unit manifest.","enum":["patch","replace"]},
    "unit_ids":{"type":"array","description":"Opaque unit IDs from an exported XLIFF manifest. Required and non-empty for patch; omitted or empty for replace.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  }
}`

const translationXLIFFImportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","locale","mode","file_id"],
  "properties":{
    "document_type":` + translationProfileJSONSchema + `,
    "document_id":{"type":"string","description":"The document_id matching the uploaded XLIFF file identity.","minLength":1,"maxLength":256},
    "locale":` + localeJSONSchema + `,
    "mode":{"description":"patch updates the explicit uploaded units and preserves other target values; replace requires the complete current unit manifest.","enum":["patch","replace"]},
    "file_id":{"type":"string","description":"Completed existing upload File ID containing the edited XLIFF. Upload edited XML first; never pass XML or a URL here.","format":"uuid"},
    "expected_target_revision":{"type":"string","description":"Exact target_revision returned by translation_xliff_export. Required for an existing target; omit only to create a currently absent target. A concurrent change causes a conflict.","minLength":1,"maxLength":256}
  }
}`

const translationListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["source_locale","locales"],
  "properties":{
    "source_locale":{"description":"Current source locale.","allOf":[` + localeJSONSchema + `]},
    "locales":{"type":"array","description":"Existing locale metadata; no translated content.","items":{"type":"object","additionalProperties":false,"required":["locale","updated_at"],"properties":{
      "locale":` + localeJSONSchema + `,
      "updated_at":{"type":["string","null"],"description":"Locale update timestamp when available.","format":"date-time"}
    }}}
  }
}`

const translationXLIFFExportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["artifact","source_locale","locale","mode"],
  "properties":{
    "artifact":{"type":"object","description":"Exported XLIFF artifact download reference.","additionalProperties":false,"required":["file_id","url","expires_at","extension","mime_type"],"properties":{
      "file_id":{"type":"string","description":"Generated artifact File ID."},"url":{"type":"string","description":"Signed download URL; retrieve the XML before expiration.","format":"uri"},"expires_at":{"type":"string","description":"Download URL expiration timestamp.","format":"date-time"},
      "extension":{"type":"string","minLength":1,"maxLength":32},"mime_type":{"type":"string","minLength":1,"maxLength":128},"file_name":{"type":"string","description":"Suggested file name.","minLength":1,"maxLength":512}
    }},
    "source_locale":` + localeJSONSchema + `,"locale":{"description":"Target locale.","allOf":[` + localeJSONSchema + `]},"mode":{"description":"Applied export mode.","enum":["patch","replace"]},"target_revision":{"type":"string","description":"Current opaque target revision. Pass unchanged as expected_target_revision on import. Omitted when the target does not exist.","minLength":1,"maxLength":256}
  }
}`

const translationXLIFFImportOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["target_revision","changed","unit_ids"],
  "properties":{"target_revision":{"type":"string","description":"Target revision after import.","minLength":1,"maxLength":256},"changed":{"type":"boolean","description":"Whether stored target values changed."},"unit_ids":{"type":"array","description":"Opaque unit IDs affected by the validated import.","uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}}
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
  "type":"object","additionalProperties":false,"required":["document_type","document_id"],
  "properties":{
    "document_type":` + translationProfileJSONSchema + `,
    "document_id":` + translationDocumentIDJSONSchema + `,
    "locale":{"description":"One target locale, read whole; it must differ from the source locale. Omit locale and locales to read every existing translation.","allOf":[` + localeJSONSchema + `]},
    "locales":{"type":"array","description":"Several target locales to read together. Needs block_ids, query or unit_ids.","minItems":1,"maxItems":64,"uniqueItems":true,"items":` + localeJSONSchema + `},
    "unit_ids":{"type":"array","description":"Unit IDs to read. Omit to read every current unit.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}},
    "block_ids":{"type":"array","description":"Read only the units of these Block or section handles, from document_read.","maxItems":256,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":160}},
    "query":{"type":"string","description":"Read only units whose text in the source or a requested locale contains this, ignoring case and markup.","minLength":1,"maxLength":200}
  }
}`

const translationUnitsGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["source_locale","units"],
  "properties":{
    "source_locale":` + localeJSONSchema + `,
    "locale":{"description":"Target locale of a one-locale read.","allOf":[` + localeJSONSchema + `]},
    "target_revision":{"type":"string","description":"One-locale read: the current opaque target revision, omitted when the target does not exist."},
    "target_revisions":{"type":"object","description":"Several locales: the current target revision per locale.","additionalProperties":{"type":"string"}},
    "units":{"type":"array","description":"Units in document order.","items":{"type":"object","additionalProperties":false,"required":["unit_id","group_id","source"],"properties":{
      "unit_id":{"type":"string"},
      "group_id":{"type":"string","description":"Group the unit belongs to, such as one block or the document metadata."},
      "context":{"type":"string","description":"Context note for the unit."},
      "source":{"type":"string","description":"Source text as ` + translationUnitMarkupJSON + ` Plain text for a one-run unit in a several-locale read."},
      "plain_source":{"type":"string","description":"Plain source text of a marked-up unit, to pass as source in translation_units_put edits."},
      "target":{"type":"string","description":"One-locale read: the current target, omitted when absent."},
      "targets":{"type":"object","description":"Several locales: the target per locale.","additionalProperties":{"type":"string"}}
    }}}
  }
}`

const translationUnitsPutInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["documents"],
  "properties":{"documents":{"type":"array","minItems":1,"maxItems":50,"description":"The documents to write. Each commits by itself, so a failure in one leaves the others written and reports which.","items":{
    "type":"object","additionalProperties":false,"required":["document_type","document_id"],
    "properties":{
      "document_type":` + translationProfileJSONSchema + `,
      "document_id":` + translationDocumentIDJSONSchema + `,
      "edits":{"type":"array","description":"Edits applied across locales in one transaction. Do not combine with locale, unit_targets or expected_target_revision.","minItems":1,"maxItems":1000,"items":{"type":"object","additionalProperties":false,"required":["source","targets"],"properties":{
        "source":{"type":"string","description":"The unit's current plain source text, without markup."},
        "unit_id":{"type":"string","description":"Unit ID, when several units share source.","minLength":1,"maxLength":256},
        "block_id":{"type":"string","description":"Block or section handle, when several units share source.","minLength":1,"maxLength":160},
        "targets":{"type":"object","description":"Target per locale. Plain text for a one-run unit; otherwise ` + translationUnitMarkupJSON + `","minProperties":1,"maxProperties":64,"additionalProperties":{"type":"string","maxLength":100000}}
      }}},
      "locale":{"description":"With unit_targets: the one target locale; it must differ from the source locale.","allOf":[` + localeJSONSchema + `]},
      "unit_targets":{"type":"object","description":"With locale: target per unit ID from translation_units_get, as ` + translationUnitMarkupJSON + ` Keep every source code with the same id and nesting, and the same line breaks and {{…}} placeholders.","minProperties":1,"maxProperties":1000,"additionalProperties":{"type":"string","maxLength":100000}},
      "expected_target_revision":{"type":"string","description":"With locale and unit_targets: exact target_revision returned by translation_units_get. Required for an existing target; omit only to create a currently absent target. A concurrent change causes a conflict.","minLength":1,"maxLength":256}
    }
  }}}
}`

func translationUnitsPutOutputSchema() json.RawMessage {
	return json.RawMessage(translationUnitsPutOutputJSONSchema)
}

const translationUnitsPutOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["documents","applied","failed"],
  "properties":{
    "documents":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["document_type","document_id","status"],"properties":{
      "document_type":{"type":"string"},
      "document_id":{"type":"string"},
      "status":{"enum":["applied","failed"],"description":"failed means nothing changed in this document."},
      "error":{"type":"string"},
      "locales":{"type":"object","description":"With edits: the result per locale.","additionalProperties":{"type":"object","additionalProperties":false,"required":["target_revision","changed"],"properties":{
        "target_revision":{"type":"string"},"changed":{"type":"boolean"}
      }}},
      "target_revision":{"type":"string","description":"With unit_targets: the target revision after the write."},
      "changed":{"type":"boolean","description":"With unit_targets: whether stored target values changed."},
      "unit_ids":{"type":"array","description":"With unit_targets: opaque unit IDs affected by the write.","uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
    }}},
    "applied":{"type":"integer","minimum":0},
    "failed":{"type":"integer","minimum":0}
  }
}`
