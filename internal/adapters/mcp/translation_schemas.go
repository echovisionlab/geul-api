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

const localeJSONSchema = `{"type":"string","description":"Exact supported locale code; aliases and case variants are not normalized.","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}`
const translationProfileJSONSchema = `{"description":"Document profile, paired with the document reference d.","allOf":[` + domainJSONSchema + `]}`

const translationTargetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d"],
  "properties":{"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Exact owning document reference, also used by document_open.","minLength":1,"maxLength":256}}
}`

const translationGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Exact owning document reference, also used by document_open.","minLength":1,"maxLength":256},"l":` + localeJSONSchema + `}
}`

const translationJobInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{"j":{"type":"string","description":"Active translation Job ID returned by translation_jobs_list or translation_regenerate.","format":"uuid"}}
}`

const translationJobsListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact document reference. Non-admin callers must provide both p and d.","minLength":1,"maxLength":256},
    "tl":{"description":"Filter by exact target locale.","allOf":[` + localeJSONSchema + `]},
    "sl":{"description":"Filter by exact source locale.","allOf":[` + localeJSONSchema + `]},
    "s":{"type":"array","description":"Active statuses to include; omission or an empty array includes both statuses.","maxItems":2,"uniqueItems":true,"items":{"enum":["queued","running"]}},
    "n":{"type":"integer","description":"Page size. Omission or zero uses 20; maximum 100.","minimum":0,"maximum":100},
    "o":{"type":"integer","description":"Number of matching jobs to skip; defaults to zero.","minimum":0},
    "k":{"description":"Sort field. Omission uses updated_at descending. Job ID is the final tie-breaker.","enum":["requested_at","updated_at","target_locale","status"]},
    "z":{"type":"boolean","description":"Descending when true, ascending when false; requires k. Defaults to false with an explicit k."}
  },
  "dependentRequired":{"p":["d"],"d":["p"],"z":["k"]}
}`

const translationRegenerateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":{"type":"array","description":"Explicit target locale selection admitted by runtime machine-translation policy; no implicit all-locales request.","minItems":1,"maxItems":32,"uniqueItems":true,"items":` + localeJSONSchema + `}
  }
}`

const translationXLIFFExportInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["p","d","l","m"],
  "properties":{
    "p":` + translationProfileJSONSchema + `,
    "d":{"type":"string","description":"Exact owning document reference.","minLength":1,"maxLength":256},
    "l":` + localeJSONSchema + `,
    "m":{"description":"patch exports the explicit u selection; replace exports the complete current unit manifest.","enum":["patch","replace"]},
    "u":{"type":"array","description":"Opaque unit IDs from an exported XLIFF manifest. Required and non-empty for patch; omitted or empty for replace.","maxItems":1000,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":256}}
  },
  "allOf":[
    {"if":{"properties":{"m":{"const":"patch"}},"required":["m"]},"then":{"required":["u"],"properties":{"u":{"minItems":1}}}},
    {"if":{"properties":{"m":{"const":"replace"}},"required":["m"]},"then":{"properties":{"u":{"maxItems":0}}}}
  ]
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

const compactJobDefinitionJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["i","p","d","tl","sl","s","o"],
  "properties":{
    "i":{"type":"string","description":"Translation Job ID.","format":"uuid"},"p":` + translationProfileJSONSchema + `,"d":{"type":"string","description":"Owning document reference.","minLength":1,"maxLength":256},
    "tl":{"description":"Job target locale.","allOf":[` + localeJSONSchema + `]},"sl":{"description":"Job source locale.","allOf":[` + localeJSONSchema + `]},
    "s":{"description":"Active Job status.","enum":["queued","running"]},
    "o":{"type":"string","description":"Correlation operation ID for the translation request.","format":"uuid"},
    "rq":{"type":"string","description":"Job request timestamp.","format":"date-time"},"st":{"type":"string","description":"Job start timestamp, when started.","format":"date-time"}
  }
}`

const translationJobCancellationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{"j":{"type":"string","description":"Cancelled translation Job ID.","format":"uuid"}}
}`

const translationJobsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["j"],
  "properties":{
    "j":{"type":"array","description":"Active translation Job metadata.","items":{"$ref":"#/$defs/job"}},
    "g":{"type":"array","description":"Pagination tuple [total matching jobs, page limit, offset, has more]. Present for translation_jobs_list.","prefixItems":[{"type":"integer","description":"Total matching jobs."},{"type":"integer","description":"Applied page limit."},{"type":"integer","description":"Applied offset."},{"type":"boolean","description":"More matching jobs follow this page."}],"items":false,"minItems":4,"maxItems":4}
  },
  "$defs":{"job":` + compactJobDefinitionJSONSchema + `}
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
