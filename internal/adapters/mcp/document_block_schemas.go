package mcp

const blockDeleteInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","block_id"],
  "properties":{
    "document_type":` + domainJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Source locale whose structure is being edited."},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"document_revision from your latest document_read or the previous edit."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision when one was returned."},
    "block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing Block handle returned by document_read."}
  }
}`

const blockTargetProperties = `"document_type":{"enum":["page","post"]},
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":` + plainLocaleJSONSchema + `,
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision for a non-source locale; omit when none was returned."}`

const blockCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["kind","document_type","document_id","locale"],
  "properties":{
    "kind":{"enum":["paragraph","heading","table"]},
    ` + blockTargetProperties + `,
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Optional, because this only adds a block."},
    "parent_block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing rich-text section; required for a Page."},
    "after_block_id":{"type":"string","maxLength":160,"description":"Existing sibling to insert after."},
    "text":{"type":"string","description":"paragraph and heading: plain text."},
    "level":{"type":"integer","enum":[1,2,3],"description":"heading only."},
    "alignment":{"type":"string","enum":["left","center","right"],"description":"heading only."},
    "rows":{"type":"array","minItems":1,"items":{"type":"array","minItems":1,"items":{"type":"string"}},"description":"table only: rows of plain-text cells, all the same length."},
    "header_row":{"type":"boolean","description":"table only: the first row is headers."},
    "header_column":{"type":"boolean","description":"table only: the first column is headers."}
  }
}`

const blockUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["kind","document_type","document_id","locale","expected_document_revision","block_id","text"],
  "properties":{
    "kind":{"enum":["paragraph","heading"]},
    ` + blockTargetProperties + `,
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"document_revision from your latest document_read or the previous edit."},
    "block_id":{"type":"string","minLength":1,"maxLength":160},
    "text":{"type":"string","description":"Replacement plain text."},
    "level":{"type":"integer","enum":[1,2,3],"description":"heading only; left out keeps it."},
    "alignment":{"type":"string","enum":["left","center","right"],"description":"heading only; left out keeps it."}
  }
}`
