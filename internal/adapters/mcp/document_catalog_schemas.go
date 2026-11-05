package mcp

import "encoding/json"

const catalogShapePropertiesJSONSchema = `
  "enum_values":{"type":"array","minItems":1,"items":{"type":["string","number","boolean"]}},
  "exclusive_minimum":{"type":"number"},"minimum":{"type":"number"},"maximum":{"type":"number"},"required":{"type":"boolean","description":"Required generated value or list identity field."},
  "kind":{"enum":["t","b","n","i","l","o"],"description":"DCDP typed value tag. Omitted for File bindings, which use fa/fd operations instead of typed values."},
  "ownership":{"enum":["shared","source","locale"]},
  "translatable":{"type":"boolean"},
  "file":{"type":"boolean","description":"Verified File binding. Attach/detach by File handle; ownership determines which locale may mutate it."},
  "item":{"$ref":"#/$defs/shape"},
  "fields":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["field","schema"],"properties":{
    "field":{"type":"string","minLength":1},"schema":{"$ref":"#/$defs/shape"}
  }}},
  "identity":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{
    "kind":{"enum":["positional","value","field","fixed"],"description":"positional: list items have empty handles; value: handle equals scalar value; field: handle equals the named item field; fixed: use the listed handles."},
    "field":{"type":"string","minLength":1},
    "handles":{"type":"array","minItems":1,"items":{"type":"string","minLength":1}}
  }}
`

const documentCatalogOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["v","p","c","d","dr","s","l","lr","le","block_kinds","fields","relations","relation_fields"],
  "properties":{
    "v":{"const":"dcdp/1"},"p":` + domainJSONSchema + `,
    "c":{"type":"string","minLength":1,"maxLength":256},"d":` + documentReferenceJSONSchema + `,
    "dr":{"type":"string","minLength":1,"maxLength":256},"tr":{"type":"string","minLength":1,"maxLength":256},
    "s":{"type":"string","minLength":1,"maxLength":35},"l":{"type":"string","minLength":1,"maxLength":35},
    "lr":{"enum":["source","non_source"]},"le":{"type":"boolean"},
    "block_rules":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["kind","category"],"properties":{
      "kind":{"type":"string"},"category":{"enum":["rich_block","page_section","page_column"]},
      "allowed_parents":{"type":"array","items":{"type":"string"},"description":"Allowed parent kinds; rich Page blocks require a rich-text section."}
    }}},
    "block_kinds":{"type":"array","items":{"type":"string","minLength":1}},
    "fields":{"type":"array","items":{"type":"object","additionalProperties":false,
      "required":["block_kind","field","ownership","translatable","file"],"properties":{
        "block_kind":{"type":"string","minLength":1},"field":{"type":"string","minLength":1},` + catalogShapePropertiesJSONSchema + `
    }}},
    "relations":{"type":"array","items":{"type":"object","additionalProperties":false,
      "required":["block_kind","relation","item_kinds"],"properties":{
        "block_kind":{"type":"string","minLength":1},"relation":{"type":"string","minLength":1},
        "item_kinds":{"type":"array","items":{"type":"string","minLength":1}}
    }}},
    "relation_fields":{"type":"array","items":{"type":"object","additionalProperties":false,
      "required":["block_kind","relation","item_kind","field","ownership","translatable","file"],"properties":{
        "block_kind":{"type":"string","minLength":1},"relation":{"type":"string","minLength":1},
        "item_kind":{"type":"string","minLength":1},"field":{"type":"string","minLength":1},` + catalogShapePropertiesJSONSchema + `
    }}}
  },
  "$defs":{"shape":{"type":"object","additionalProperties":false,"required":["ownership","translatable","file"],"properties":{` + catalogShapePropertiesJSONSchema + `}}}
}`

func documentCatalogInputSchema() json.RawMessage {
	return json.RawMessage(documentCatalogInputJSONSchema)
}

const documentCatalogInputJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["p","d","l"],
  "properties":{
    "p":` + domainJSONSchema + `,
    "d":` + documentReferenceJSONSchema + `,
    "l":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "k":{"type":"array","description":"Return only these block kinds, such as [\"file\",\"paragraph\"]. Omit for the whole catalog.","minItems":1,"maxItems":64,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":64}}
  }
}`

func documentCatalogOutputSchema() json.RawMessage {
	return json.RawMessage(documentCatalogOutputJSONSchema)
}
