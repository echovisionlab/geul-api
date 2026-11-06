package mcp

const domainJSONSchema = `{"enum":["post","page","work","program_event","release","artist","label","menu","email_template","email_layout","campaign","form","privacy","terms","post_series"]}`

// plainLocaleJSONSchema is a locale code where its property's name says enough.
const plainLocaleJSONSchema = `{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"}`

const documentReferenceJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$","description":"Canonical document UUID returned by document_list or another authenticated document surface; never a slug or URL."}`

const documentMetadataUpdateInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision"],
  "properties":{
    "document_type":{"enum":["post","work","page","program_event","release","terms","privacy"]},
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":` + plainLocaleJSONSchema + `,
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

// documentEnvelopeOutputProperties is what every document read returns about
// the document itself.
const documentEnvelopeOutputProperties = `
    "document_type":` + domainJSONSchema + `,
    "catalog_fingerprint":{"type":"string","minLength":1,"maxLength":256},
    "document_id":` + uuidJSONSchema + `,
    "document_revision":{"type":"string","minLength":1,"maxLength":256},
    "target_revision":{"type":"string","minLength":1,"maxLength":256},
    "source_locale":` + plainLocaleJSONSchema + `,
    "locale":` + plainLocaleJSONSchema + `,
    "locale_role":{"enum":["source","non_source"]},
    "locale_exists":{"type":"boolean"}`

const readFieldSelectionJSONSchema = `{"type":"object","additionalProperties":false,"required":["block_id","field"],"properties":{
  "block_id":{"type":"string","minLength":1,"maxLength":160},
  "relation_id":{"type":"string","minLength":1,"maxLength":120,"description":"With item_id, reads a relation item's field."},
  "item_id":{"type":"string","minLength":1,"maxLength":160},
  "field":{"type":"string","minLength":1,"maxLength":120}
}}`

const readInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","mode"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":` + plainLocaleJSONSchema + `,
    "mode":{"enum":["metadata","outline","blocks","fields"],"description":"metadata: revisions and locale state only. outline: every block with a preview. blocks: full values of block_ids. fields: only the selected fields."},
    "block_ids":{"type":"array","maxItems":256,"description":"Required and non-empty for blocks mode; omit in outline and fields modes.","items":{"type":"string","minLength":1,"maxLength":160}},
    "fields":{"type":"array","maxItems":256,"description":"Required and non-empty for fields mode; omit in outline and blocks modes.","items":` + readFieldSelectionJSONSchema + `},
    "limit":{"type":"integer","minimum":0,"maximum":256},
    "cursor":{"type":"string","maxLength":4096,"description":"next_cursor from the previous page."},
    "query":{"type":"string","minLength":1,"maxLength":200,"description":"Outline only: list just the blocks whose text in this locale contains query, ignoring case, with runs of spaces counting as one; each preview shows the text around the match."}
  }
}`

// documentValueDefinitions are the value shapes operations write and reads
// return. Each alternative is an object told apart by its type, which connectors
// project cleanly.
const documentValueDefinitions = `
    "inlineItem":{"description":"One piece of rich text, told apart by type.","anyOf":[
      {"type":"object","additionalProperties":false,"required":["type","text"],"properties":{"type":{"const":"text"},"text":{"type":"string"}}},
      {"type":"object","additionalProperties":false,"required":["type","content"],"properties":{"type":{"enum":["bold","italic","underline","strike","code"]},"content":{"type":"array","items":{"$ref":"#/$defs/inlineItem"}}}},
      {"type":"object","additionalProperties":false,"required":["type","color","content"],"properties":{"type":{"enum":["text_color","background_color"]},"color":{"type":"string","minLength":1,"maxLength":64},"content":{"type":"array","items":{"$ref":"#/$defs/inlineItem"}}}},
      {"type":"object","additionalProperties":false,"required":["type","url","content"],"properties":{"type":{"const":"link"},"url":{"type":"string","minLength":1,"maxLength":2048},"content":{"type":"array","items":{"$ref":"#/$defs/inlineItem"}}}},
      {"type":"object","additionalProperties":false,"required":["type"],"properties":{"type":{"const":"hard_break"}}},
      {"type":"object","additionalProperties":false,"required":["type","key"],"properties":{"type":{"const":"site_variable"},"key":{"enum":["site.title","site.url","site.companyName","contact.legalEmail","contact.email"]},"linked":{"const":true,"description":"Only site.url and contact.* may be linked."}}},
      {"type":"object","additionalProperties":false,"required":["type","expression"],"properties":{"type":{"const":"math"},"expression":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["type","handle"],"properties":{"type":{"const":"placeholder"},"handle":{"type":"string","minLength":1,"maxLength":160}}}
    ]},
    "value":{"description":"A typed field value. document_catalog names the type each field takes.","anyOf":[
      {"type":"object","additionalProperties":false,"required":["type","text"],"properties":{"type":{"const":"text"},"text":{"type":"string"}}},
      {"type":"object","additionalProperties":false,"required":["type","number"],"properties":{"type":{"const":"number"},"number":{"type":"string","minLength":1,"description":"Canonical decimal string."}}},
      {"type":"object","additionalProperties":false,"required":["type","boolean"],"properties":{"type":{"const":"boolean"},"boolean":{"type":"boolean"}}},
      {"type":"object","additionalProperties":false,"required":["type","content"],"properties":{"type":{"const":"rich_text"},"content":{"type":"array","items":{"$ref":"#/$defs/inlineItem"}}}},
      {"type":"object","additionalProperties":false,"required":["type","items"],"properties":{"type":{"const":"list"},"items":{"type":"array","items":{"$ref":"#/$defs/listItem"}}}},
      {"type":"object","additionalProperties":false,"required":["type","fields"],"properties":{"type":{"const":"object"},"fields":{"type":"array","items":{"$ref":"#/$defs/namedField"}}}}
    ]},
    "listItem":{"type":"object","additionalProperties":false,"required":["value"],"properties":{"item_id":{"type":"string","minLength":1,"maxLength":160,"description":"Stable item handle; leave out on every item of a positional list."},"value":{"$ref":"#/$defs/value"}}},
    "namedField":{"type":"object","additionalProperties":false,"required":["field","value"],"properties":{"field":{"type":"string","minLength":1,"maxLength":120},"value":{"$ref":"#/$defs/value"}}},
    "fieldPathSegment":{"type":"object","additionalProperties":false,"description":"One step into a nested value: exactly one of field and item_id.","properties":{"field":{"type":"string","minLength":1,"maxLength":120},"item_id":{"type":"string","minLength":1,"maxLength":160}}}`

// fieldAddressProperties addresses a field: on a block, or with relation_id and
// item_id on a relation item, and with path inside a nested value.
const fieldAddressProperties = `"block_id":{"type":"string","minLength":1,"maxLength":160},"relation_id":{"type":"string","minLength":1,"maxLength":120},"item_id":{"type":"string","minLength":1,"maxLength":160},"field":{"type":"string","minLength":1,"maxLength":120},"path":{"type":"array","minItems":1,"maxItems":32,"items":{"$ref":"#/$defs/fieldPathSegment"}}`

const blockHandleProperty = `{"type":"string","minLength":1,"maxLength":160}`

const mutationInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","operations"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":` + plainLocaleJSONSchema + `,
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"document_revision from your latest document_read or the previous edit."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target_revision returned for a non-source locale; omit when none was returned."},
    "operations":{"type":"array","minItems":1,"maxItems":100,"description":"Operations applied together: all or none. To change words, links or marks use document_text_replace or document_text_format instead.","items":{"$ref":"#/$defs/operation"}},
    "dry_run":{"type":"boolean","default":false,"description":"Check the operations and return them normalized, changing nothing."}
  },
  "$defs":{` + documentValueDefinitions + `,
    "operation":{"description":"One mutation, told apart by op. Use handles from document_read, and new unique handles for inserted blocks and items. A Page's rich blocks need a rich-text section as parent; heading level is a text value. See document_catalog for block kinds and field types.","anyOf":[
      {"type":"object","additionalProperties":false,"required":["op","block_id","field","value"],"properties":{"op":{"const":"set_field"},` + fieldAddressProperties + `,"value":{"$ref":"#/$defs/value"}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","field"],"properties":{"op":{"const":"unset_field"},` + fieldAddressProperties + `}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","kind"],"description":"Set its content with set_field in the same batch.","properties":{"op":{"const":"insert_block"},"block_id":` + blockHandleProperty + `,"kind":{"type":"string","minLength":1},"parent_block_id":` + blockHandleProperty + `,"after_block_id":` + blockHandleProperty + `}},
      {"type":"object","additionalProperties":false,"required":["op","block_id"],"properties":{"op":{"const":"delete_block"},"block_id":` + blockHandleProperty + `}},
      {"type":"object","additionalProperties":false,"required":["op","block_id"],"properties":{"op":{"const":"move_block"},"block_id":` + blockHandleProperty + `,"parent_block_id":` + blockHandleProperty + `,"after_block_id":` + blockHandleProperty + `}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","kind"],"properties":{"op":{"const":"replace_block_kind"},"block_id":` + blockHandleProperty + `,"kind":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","relation_id","item_id","kind"],"properties":{"op":{"const":"insert_relation_item"},"block_id":` + blockHandleProperty + `,"relation_id":{"type":"string","minLength":1},"item_id":{"type":"string","minLength":1},"kind":{"type":"string","minLength":1},"after_item_id":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","relation_id","item_id"],"properties":{"op":{"const":"delete_relation_item"},"block_id":` + blockHandleProperty + `,"relation_id":{"type":"string","minLength":1},"item_id":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","relation_id","item_id","target_block_id","target_relation_id"],"properties":{"op":{"const":"move_relation_item"},"block_id":` + blockHandleProperty + `,"relation_id":{"type":"string","minLength":1},"item_id":{"type":"string","minLength":1},"target_block_id":` + blockHandleProperty + `,"target_relation_id":{"type":"string","minLength":1},"after_item_id":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","field","file_id"],"description":"Attach a verified File.","properties":{"op":{"const":"attach_file"},` + fieldAddressProperties + `,"file_id":{"type":"string","minLength":1}}},
      {"type":"object","additionalProperties":false,"required":["op","block_id","field"],"properties":{"op":{"const":"detach_file"},` + fieldAddressProperties + `}},
      {"type":"object","additionalProperties":false,"required":["op"],"description":"Create or delete the requested non-source translation. Must be the only operation in its batch.","properties":{"op":{"enum":["create_translation","delete_translation"]}}}
    ]}
  }
}`

const validationOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "operations":{"type":"array","description":"The normalized operations, ready to pass to document_apply.","items":{"type":"object"}},
    "issues":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["operation_index","code","handle","message"],"properties":{
      "operation_index":{"type":"integer","minimum":0},"code":{"type":"string"},"handle":{"type":"string"},"message":{"type":"string"}
    }}},
    "conflict":{"type":"object","additionalProperties":false,"required":["code","current_document_revision","current_target_revision","affected_handles"],"properties":{
      "code":{"enum":["document_revision_conflict","target_revision_conflict"]},"current_document_revision":{"type":"string"},"current_target_revision":{"type":["string","null"]},"affected_handles":{"type":"array","items":{"type":"string"}}
    }}
  }
}`

// acceptedChangesProperty lists what an accepted mutation did, operation by
// operation.
const acceptedChangesProperty = `"changes":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["operation_index","op","affected_handles"],"properties":{
      "operation_index":{"type":"integer","minimum":0},
      "op":{"type":"string"},
      "affected_handles":{"type":"array","items":{"type":"string"}}
    }}}`

const acceptedOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_revision","changes"],
  "properties":{
    "document_revision":{"type":"string","minLength":1,"maxLength":256},
    "target_revision":{"type":"string","minLength":1,"maxLength":256},
    ` + acceptedChangesProperty + `
  }
}`

const focusedMutationOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_revision","changes"],
  "properties":{
    "document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Current document revision after the operation; unchanged for target-only changes and semantic no-ops."},
    "target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Current target revision after the operation for an existing non-source locale; unchanged for semantic no-ops."},
    "block_id":{"type":"string","format":"uuid","description":"Canonical UUID assigned to the newly created Block."},
    ` + acceptedChangesProperty + `
  }
}`

const projectionOutputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","catalog_fingerprint","document_id","document_revision","source_locale","locale","locale_role","locale_exists"],
  "properties":{` + documentEnvelopeOutputProperties + `,
    "mode":{"enum":["outline","blocks","fields"],"description":"Left out for mode=metadata, which returns no blocks."},
    "query":{"type":"string"},
    "blocks":{"type":["array","null"],"items":{"anyOf":[{"$ref":"#/$defs/block"},{"$ref":"#/$defs/outlineBlock"}]}},
    "next_cursor":{"type":["string","null"]}
  },
  "$defs":{` + documentValueDefinitions + `,
    "file":{"type":"object","additionalProperties":false,"required":["field","file_id"],"properties":{"field":{"type":"string"},"path":{"type":"array","items":{"$ref":"#/$defs/fieldPathSegment"}},"file_id":{"type":"string"}}},
    "relationItem":{"type":"object","additionalProperties":false,"required":["item_id","kind","order"],"properties":{
      "item_id":{"type":"string"},"kind":{"type":"string"},"order":{"type":"integer"},
      "shared_fields":{"type":"array","items":{"$ref":"#/$defs/namedField"}},
      "localized_fields":{"type":"array","items":{"$ref":"#/$defs/namedField"}},
      "files":{"type":"array","items":{"$ref":"#/$defs/file"}}
    }},
    "relation":{"type":"object","additionalProperties":false,"required":["relation_id","items"],"properties":{
      "relation_id":{"type":"string"},"items":{"type":"array","items":{"$ref":"#/$defs/relationItem"}}
    }},
    "block":{"type":"object","additionalProperties":false,"description":"A block with its field values, in blocks and fields modes.","required":["block_id","kind","order"],"properties":{
      "block_id":{"type":"string"},"kind":{"type":"string"},"parent_block_id":{"type":"string"},"order":{"type":"integer"},
      "shared_fields":{"type":"array","items":{"$ref":"#/$defs/namedField"}},
      "localized_fields":{"type":"array","items":{"$ref":"#/$defs/namedField"}},
      "files":{"type":"array","items":{"$ref":"#/$defs/file"}},
      "relations":{"type":"array","items":{"$ref":"#/$defs/relation"}}
    }},
    "outlineBlock":{"type":"object","additionalProperties":false,"description":"A block's place and a preview of its text, in outline mode.","required":["block_id","kind","order"],"properties":{
      "block_id":{"type":"string"},"kind":{"type":"string"},"parent_block_id":{"type":"string"},"order":{"type":"integer"},"preview":{"type":"string"},
      "relations":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["relation_id","items"],"properties":{
        "relation_id":{"type":"string"},
        "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["item_id","kind","order"],"properties":{"item_id":{"type":"string"},"kind":{"type":"string"},"order":{"type":"integer"}}}}
      }}}
    }}
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
          "type":"object","additionalProperties":false,"required":["document_type","document_id","locale","mode"],
          "properties":{
            "document_type":` + domainJSONSchema + `,
            "document_id":` + uuidJSONSchema + `,
            "locale":` + plainLocaleJSONSchema + `,
            "mode":{"enum":["outline","blocks","fields"]},
            "block_ids":{"type":"array","maxItems":256,"items":{"type":"string","minLength":1,"maxLength":160}},
            "fields":{"type":"array","maxItems":256,"items":` + readFieldSelectionJSONSchema + `},
            "limit":{"type":"integer","minimum":0,"maximum":256}
          }
        }
      }
    },
    "instructions":{"type":"string","minLength":1}
  }
}`
