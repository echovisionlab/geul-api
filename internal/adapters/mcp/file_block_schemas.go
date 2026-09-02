package mcp

const fileBlockDocumentTypeJSONSchema = `{"enum":["post","page","work","program_event"]}`

const fileBlockUUIDJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"}`

const documentFileAddInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","file_id"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$","description":"Current source locale whose document structure is being edited."},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current target revision when one was returned."},
    "parent_block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Existing parent Block handle. Required for a File Block inside a Page rich-text section."},
    "after_block_id":{"type":"string","maxLength":160,"description":"Existing sibling Block handle after which to insert the File Block."},
    "file_id":` + fileBlockUUIDJSONSchema + `
  },
  "allOf":[{"if":{"properties":{"document_type":{"const":"page"}},"required":["document_type"]},"then":{"required":["parent_block_id"]}}]
}`

const documentFileReplaceInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","block_id","file_id"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256},
    "block_id":` + fileBlockUUIDJSONSchema + `,
    "file_id":` + fileBlockUUIDJSONSchema + `
  }
}`

const documentFileRemoveInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","locale","expected_document_revision","block_id"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "expected_document_revision":{"type":"string","minLength":1,"maxLength":256,"description":"Exact current document revision returned by document_open or document_read."},
    "expected_target_revision":{"type":"string","minLength":1,"maxLength":256},
    "block_id":` + fileBlockUUIDJSONSchema + `
  }
}`

const documentFileMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["dr","c"],
  "properties":{
    "dr":{"type":"string","minLength":1,"maxLength":256,"description":"New document revision."},
    "tr":{"type":"string","minLength":1,"maxLength":256},
    "block_id":{"type":"string","format":"uuid","description":"Canonical UUID assigned to a newly added File Block."},
    "c":{"type":"array","items":{"type":"array","prefixItems":[
      {"type":"integer","minimum":0},{"enum":["bi","fa","bd"]},{"type":"array","items":{"type":"string"}}
    ],"items":false,"minItems":3,"maxItems":3}}
  }
}`

const documentFileDownloadPolicyGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","block_id"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "block_id":` + fileBlockUUIDJSONSchema + `
  }
}`

const documentFileDownloadPolicyUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","block_id","expected_file_id","audience"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "block_id":` + fileBlockUUIDJSONSchema + `,
    "expected_file_id":` + fileBlockUUIDJSONSchema + `,
    "audience":{"enum":["disabled","public","authenticated","restricted"]},
    "audience_segment_ids":{"type":"array","maxItems":20,"uniqueItems":true,"items":` + fileBlockUUIDJSONSchema + `}
  }
}`

const documentFileDownloadPolicyOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","block_id","reference_path","file_id","audience","audience_segments"],
  "properties":{
    "document_type":` + fileBlockDocumentTypeJSONSchema + `,
    "document_id":` + documentReferenceJSONSchema + `,
    "block_id":` + fileBlockUUIDJSONSchema + `,
    "reference_path":{"const":"file"},
    "file_id":` + fileBlockUUIDJSONSchema + `,
    "audience":{"enum":["disabled","public","authenticated","restricted"]},
    "audience_segments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + fileBlockUUIDJSONSchema + `,"name":{"type":"string"}}}}
  }
}`

const fileUsageListInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["file_id"],
  "properties":{
    "file_id":` + fileBlockUUIDJSONSchema + `,
    "page_size":{"type":"integer","minimum":1,"maximum":100,"default":20},
    "page_token":{"type":"string"}
  }
}`

const fileUsageListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["file_id","usages","total"],
  "properties":{
    "file_id":` + fileBlockUUIDJSONSchema + `,
    "usages":{"type":"array","items":{"type":"object","additionalProperties":false,
      "required":["domain","entity_id","reference_path","count"],
      "properties":{"domain":{"type":"string"},"entity_id":{"type":"string"},"reference_path":{"type":"string"},"block_id":{"type":"string"},"count":{"type":"integer","minimum":1},"block_type":{"type":"string"},"title":{"type":"string"},"link":{"type":"string"}}
    }},
    "total":{"type":"integer","minimum":0},"next_page_token":{"type":"string"}
  }
}`
