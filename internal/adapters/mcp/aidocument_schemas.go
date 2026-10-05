package mcp

const domainJSONSchema = `{"enum":["post","page","work","program_event","release","artist","label","menu","email_template","email_layout","campaign","form","privacy","terms","post_series"]}`

const documentReferenceJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Canonical document UUID returned by document_list or another authenticated document surface; never a slug or URL."}`

const documentListInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["p"],
  "properties":{
    "p":{"enum":["post","work","page","program_event","release","artist"],"description":"The discoverable DCDP document domain."},
    "q":{"type":"string","maxLength":200,"description":"Optional case-insensitive source-title substring, or Artist source-name or real-name substring."},
    "limit":{"type":"integer","minimum":1,"maximum":50,"default":20},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}
  }
}`

const documentListOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["documents","total","next_offset"],
  "properties":{
    "documents":{"type":"array","items":{
      "type":"object","additionalProperties":false,
      "required":["p","d","title","source_locale","status","updated_at"],
      "properties":{
        "p":{"enum":["post","work","page","program_event","release","artist"]},
        "d":` + uuidJSONSchema + `,
        "title":{"type":"string"},
        "slug":{"type":"string"},
        "source_locale":{"type":"string","minLength":1,"maxLength":35},
        "status":{"type":"string","minLength":1},
        "updated_at":{"type":"string","format":"date-time"},
        "configuration_revision":{"type":"string","format":"uuid","description":"Post settings revision for post_settings_update. Separate from the content document revision."}
      },
      "allOf":[{"if":{"properties":{"p":{"const":"post"}},"required":["p"]},"then":{"required":["configuration_revision"]}}]
    }},
    "total":{"type":"integer","minimum":0},
    "next_offset":{"type":["integer","null"],"minimum":0}
  }
}`

const documentMetadataUpdateInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision"],
  "properties":{
    "document_type":{"enum":["post","work","page","program_event","release"]},
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256},
    "title":{"type":"string"},
    "summary":{"type":"string"},
    "clear_summary":{"type":"boolean","default":false},
    "category_ids":{"type":"array","maxItems":256,"uniqueItems":true,"description":"IDs from reference_search with reference_type=category.","items":` + uuidJSONSchema + `},
    "tag_ids":{"type":"array","maxItems":256,"uniqueItems":true,"description":"IDs from reference_search with reference_type=tag.","items":` + uuidJSONSchema + `},
    "document_layout":` + managementDocumentLayoutJSONSchema + `
  }
}`

const openInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["p","d","l"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":` + documentReferenceJSONSchema + `,
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}
  }
}`

const openOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["v","p","c","d","dr","s","l","lr","le"],
  "properties":{
    "v":{"const":"dcdp/1"},
    "p":` + domainJSONSchema + `,
    "c":{"type":"string","minLength":1,"maxLength":256},
    "d":` + uuidJSONSchema + `,
    "dr":{"type":"string","minLength":1,"maxLength":256},
    "tr":{"type":"string","minLength":1,"maxLength":256},
    "s":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "lr":{"enum":["source","non_source"]},
    "le":{"type":"boolean"}
  }
}`

const readInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["p","d","l","m"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":` + documentReferenceJSONSchema + `,
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "m":{"enum":["outline","blocks","fields"]},
    "b":{"type":"array","maxItems":256,"items":{"type":"string","minLength":1,"maxLength":160}},
    "f":{"type":"array","maxItems":256,"items":{"oneOf":[
      {"type":"array","prefixItems":[{"type":"string"},{"type":"string"}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"type":"string"},{"type":"string"},{"type":"string"},{"type":"string"}],"items":false,"minItems":4,"maxItems":4}
    ]}},
    "n":{"type":"integer","minimum":0,"maximum":256},
    "c":{"type":"string","maxLength":4096}
  }
}`

// Advertise compact inputs as homogeneous arrays for connector compatibility.
// The server decoder enforces each tag's exact positions, length, and types.
const mutationInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["v","p","d","l","edr","o"],
  "properties":{
    "v":{"const":"dcdp/1"},
    "p":` + domainJSONSchema + `,
    "d":` + documentReferenceJSONSchema + `,
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "edr":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision dr returned by document_open or document_read."},
    "etr":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision tr returned for a non-source locale; omit when tr was not returned."},
    "o":{"type":"array","minItems":1,"maxItems":100,"description":"Typed DCDP operations for advanced batches. For ordinary plain-text paragraph writes, use the focused paragraph tools. A prior document_validate dry run is optional.","items":{"$ref":"#/$defs/operation"}}
  },
  "$defs":{
    "operation":{
      "type":"array","minItems":1,"maxItems":7,"items":{"$ref":"#/$defs/compactPayload"},
      "description":"One compact typed mutation. Preserve the following exact tag, positions, length, and types; the server decoder validates them. Set field: [\"fs\",fieldTarget,typedValue]. For paragraph text use field content and value [\"i\",[[\"t\",text]]]. Unset field: [\"fu\",fieldTarget]. Insert block: [\"bi\",newBlockHandle,blockKind,parentBlockHandle,afterBlockHandle]; set content with fs in the same batch. Delete block: [\"bd\",blockHandle]. Move block: [\"bm\",blockHandle,parentBlockHandle,afterBlockHandle]. Replace block kind: [\"bk\",blockHandle,newBlockKind]. Insert relation item: [\"ri\",blockHandle,relationHandle,newItemHandle,itemKind,afterItemHandle]. Delete relation item: [\"rd\",blockHandle,relationHandle,itemHandle]. Move relation item: [\"rm\",sourceBlockHandle,sourceRelationHandle,itemHandle,targetBlockHandle,targetRelationHandle,afterItemHandle]. Attach verified File: [\"fa\",fieldTarget,fileHandle]. Detach File: [\"fd\",fieldTarget]. Translation lifecycle: [\"lc\"] creates the requested non-source translation; [\"ld\"] deletes it. Either must be the only operation in its batch. Use stable handles from document_read and new unique stable handles for inserted blocks/items. Use the empty string when no parent, predecessor, relation, or relation item applies."
    },
    "compactPayload":{
      "description":"Compact wire payloads use strings, booleans, and nested arrays. Field target: [block,\"\",\"\",field] for a block field or [block,relation,item,field] for a relation-item field; an optional fifth item is a nested field path of [\"f\",field] or [\"i\",stableItemHandle] segments. Typed values: [\"t\",text], [\"b\",boolean], [\"n\",canonical-number-string], [\"i\",inline-items], [\"l\",[[optionalStableItemHandle,typedValue],...]], or [\"o\",[[field,typedValue],...]]. Inline items: [\"t\",text], [mark,inline-items] where mark is b/em/u/s/code, [colorMark,color,inline-items] where colorMark is fg/bg, [\"a\",URL,inline-items], [\"br\"], [\"math\",expression], or [\"ph\",placeholderHandle]. Do not send untagged field scalars, null, objects, or native JSON numbers; numeric values use canonical decimal strings. The server decoder validates all tag-specific slot constraints.",
      "anyOf":[
        {"type":"string"},
        {"type":"boolean"},
        {"type":"array","items":{"$ref":"#/$defs/compactPayload"}}
      ]
    }
  }
}`

const validationOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "o":{"type":"array","items":{"type":"array"}},
    "i":{"type":"array","items":{"type":"array","prefixItems":[
      {"type":"integer","minimum":0},{"type":"string"},{"type":"string"},{"type":"string"}
    ],"items":false,"minItems":4,"maxItems":4}},
    "x":{"type":"array","prefixItems":[
      {"enum":["document_revision_conflict","target_revision_conflict"]},{"type":"string"},{"type":["string","null"]},{"type":"array","items":{"type":"string"}}
    ],"items":false,"minItems":4,"maxItems":4}
  }
}`

const acceptedOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["dr","c"],
  "properties":{
    "dr":{"type":"string","minLength":1,"maxLength":256},
    "tr":{"type":"string","minLength":1,"maxLength":256},
    "c":{"type":"array","items":{"type":"array","prefixItems":[
      {"type":"integer","minimum":0},
      {"enum":["fs","fu","bi","bd","bm","bk","ri","rd","rm","fa","fd","lc","ld"]},
      {"type":"array","items":{"type":"string"}}
    ],"items":false,"minItems":3,"maxItems":3}}
  }
}`

const paragraphCreateInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","text"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Locale to edit."},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision for a non-source locale; omit when none was returned."},
    "parent_block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing parent Block handle returned by document_read. Required for Page documents and must identify a rich-text section."},
    "after_block_id":{"type":"string","maxLength":160,"description":"Existing sibling Block handle after which to insert. Omit to insert at the beginning of the selected parent, or at the top level when parent_block_id is omitted."},
    "text":{"type":"string","description":"Plain text for the new Paragraph Block."}
  },
  "allOf":[{"if":{"properties":{"document_type":{"const":"page"}},"required":["document_type"]},"then":{"required":["parent_block_id"]}}]
}`

const paragraphUpdateInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","block_id","text"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Locale to edit."},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision for a non-source locale; omit when none was returned."},
    "block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing Paragraph Block handle returned by document_read."},
    "text":{"type":"string","description":"Replacement plain text for the Paragraph Block."}
  }
}`

const blockDeleteInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","block_id"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Source locale whose structure is being edited."},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision when one was returned."},
    "block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing Block handle returned by document_read."}
  }
}`

const focusedMutationOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["dr","c"],
  "properties":{
    "dr":{"type":"string","minLength":1,"maxLength":256,"description":"Current document revision after the operation; unchanged for target-only changes and semantic no-ops."},
    "tr":{"type":"string","minLength":1,"maxLength":256,"description":"Current target revision after the operation for an existing non-source locale; unchanged for semantic no-ops."},
    "block_id":{"type":"string","format":"uuid","description":"Canonical UUID assigned to the newly created Paragraph Block."},
    "c":{"type":"array","items":{"type":"array","prefixItems":[
      {"type":"integer","minimum":0},
      {"enum":["fs","fu","bi","bd"]},
      {"type":"array","items":{"type":"string"}}
    ],"items":false,"minItems":3,"maxItems":3}}
  }
}`

const projectionOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["v","p","c","d","dr","s","l","lr","le","m","n","next"],
  "properties":{
    "v":{"const":"dcdp/1"},
    "p":` + domainJSONSchema + `,
    "c":{"type":"string"},
    "d":` + uuidJSONSchema + `,
    "dr":{"type":"string"},
    "tr":{"type":"string"},
    "s":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "lr":{"enum":["source","non_source"]},
    "le":{"type":"boolean"},
    "m":{"enum":["outline","blocks","fields"]},
    "n":{"type":["array","null"],"items":{"$ref":"#/$defs/node"}},
    "next":{"type":["string","null"]}
  },
  "$defs":{
    "inline":{"oneOf":[
      {"type":"array","prefixItems":[{"const":"t"},{"type":"string"}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"enum":["b","em","u","s","code"]},{"type":"array","items":{"$ref":"#/$defs/inline"},"minItems":1}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"enum":["fg","bg"]},{"type":"string","minLength":1,"maxLength":64},{"type":"array","items":{"$ref":"#/$defs/inline"},"minItems":1}],"items":false,"minItems":3,"maxItems":3},
      {"type":"array","prefixItems":[{"const":"a"},{"type":"string","minLength":1,"maxLength":2048},{"type":"array","items":{"$ref":"#/$defs/inline"},"minItems":1}],"items":false,"minItems":3,"maxItems":3},
      {"type":"array","prefixItems":[{"const":"br"}],"items":false,"minItems":1,"maxItems":1},
      {"type":"array","prefixItems":[{"enum":["math","ph"]},{"type":"string"}],"items":false,"minItems":2,"maxItems":2}
    ]},
    "listItem":{"type":"array","prefixItems":[{"type":"string","maxLength":160},{"$ref":"#/$defs/value"}],"items":false,"minItems":2,"maxItems":2},
    "objectField":{"type":"array","prefixItems":[{"type":"string","minLength":1,"maxLength":120},{"$ref":"#/$defs/value"}],"items":false,"minItems":2,"maxItems":2},
    "value":{"oneOf":[
      {"type":"array","prefixItems":[{"enum":["t","n"]},{"type":"string"}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"const":"b"},{"type":"boolean"}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"const":"i"},{"type":"array","items":{"$ref":"#/$defs/inline"}}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"const":"l"},{"type":"array","items":{"$ref":"#/$defs/listItem"}}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"const":"o"},{"type":"array","items":{"$ref":"#/$defs/objectField"}}],"items":false,"minItems":2,"maxItems":2}
    ]},
    "fieldPathSegment":{"oneOf":[
      {"type":"array","prefixItems":[{"const":"f"},{"type":"string","minLength":1,"maxLength":120}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"const":"i"},{"type":"string","minLength":1,"maxLength":160}],"items":false,"minItems":2,"maxItems":2}
    ]},
    "fieldPath":{"type":"array","minItems":1,"maxItems":32,"items":{"$ref":"#/$defs/fieldPathSegment"}},
    "field":{"type":"array","prefixItems":[{"type":"string"},{"$ref":"#/$defs/value"}],"items":false,"minItems":2,"maxItems":2},
    "file":{"oneOf":[
      {"type":"array","prefixItems":[{"type":"string"},{"type":"string"}],"items":false,"minItems":2,"maxItems":2},
      {"type":"array","prefixItems":[{"type":"string"},{"$ref":"#/$defs/fieldPath"},{"type":"string"}],"items":false,"minItems":3,"maxItems":3}
    ]},
    "relationItem":{"type":"array","prefixItems":[
      {"type":"string"},{"type":"string"},{"type":"integer"},
      {"type":["array","null"],"items":{"$ref":"#/$defs/field"}},
      {"type":["array","null"],"items":{"$ref":"#/$defs/field"}},
      {"type":["array","null"],"items":{"$ref":"#/$defs/file"}}
    ],"items":false,"minItems":6,"maxItems":6},
    "relation":{"type":"array","prefixItems":[
      {"type":"string"},{"type":["array","null"],"items":{"$ref":"#/$defs/relationItem"}}
    ],"items":false,"minItems":2,"maxItems":2},
    "node":{"type":"array","prefixItems":[
      {"type":"string"},{"type":"string"},{"type":"string"},{"type":"integer"},
      {"type":["array","null"],"items":{"$ref":"#/$defs/field"}},
      {"type":["array","null"],"items":{"$ref":"#/$defs/field"}},
      {"type":["array","null"],"items":{"$ref":"#/$defs/file"}},
      {"type":["array","null"],"items":{"$ref":"#/$defs/relation"}}
    ],"items":false,"minItems":8,"maxItems":8}
  }
}`

const documentSyncOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["status","reason","applied","current_document_revision","current_target_revision","affected_handles","discard_previous_pages","read","instructions"],
  "properties":{
    "status":{"const":"sync_required"},
    "reason":{"enum":["read_continuation_invalid","document_revision_changed","target_revision_changed"]},
    "applied":{"const":false},
    "current_document_revision":{"type":"string","minLength":1},
    "current_target_revision":{"type":["string","null"]},
    "affected_handles":{"type":"array","items":{"type":"string"},"description":"Pending operation targets from the conflict; not handles known to have changed."},
    "discard_previous_pages":{"type":"boolean"},
    "read":{
      "type":"object","additionalProperties":false,"required":["tool","arguments"],
      "properties":{
        "tool":{"const":"document_read"},
        "arguments":{
          "type":"object","additionalProperties":false,"required":["p","d","l","m"],
          "properties":{
            "p":` + domainJSONSchema + `,
            "d":` + uuidJSONSchema + `,
            "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
            "m":{"enum":["outline","blocks","fields"]},
            "b":{"type":"array","maxItems":256,"items":{"type":"string","minLength":1,"maxLength":160}},
            "f":{"type":"array","maxItems":256,"items":{"oneOf":[
              {"type":"array","prefixItems":[{"type":"string"},{"type":"string"}],"items":false,"minItems":2,"maxItems":2},
              {"type":"array","prefixItems":[{"type":"string"},{"type":"string"},{"type":"string"},{"type":"string"}],"items":false,"minItems":4,"maxItems":4}
            ]}},
            "n":{"type":"integer","minimum":0,"maximum":256}
          }
        }
      }
    },
    "instructions":{"type":"string","minLength":1}
  }
}`
