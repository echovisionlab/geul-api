package mcp

const documentSearchInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["query"],
  "properties":{
    "query":{"type":"string","minLength":1,"maxLength":200,"description":"Text to find, ignoring case; runs of spaces count as one."},
    "document_type":{"allOf":[` + domainJSONSchema + `],"description":"Search only this document type. Omit to search every type."},
    "locales":{"type":"array","minItems":1,"maxItems":64,"uniqueItems":true,"description":"Search these locales instead of each document's source locale.","items":` + localeJSONSchema + `},
    "block_limit":{"type":"integer","minimum":1,"maximum":256,"default":20,"description":"Most matching blocks to list per document and locale; more_blocks counts the rest."},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0,"description":"next_offset from the previous call."},
    "exhaustive":{"type":"boolean","default":false,"description":"Read every document instead of using the search index. Slower, and next_offset then counts through the lists, so keep one setting through a search. For checking that nothing was missed before a site-wide change."}
  }
}`

const documentSearchOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["matches","scanned","next_offset"],
  "properties":{
    "matches":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["document_type","document_id","title","source_locale"],"properties":{
      "document_type":{"type":"string"},
      "document_id":{"type":"string"},
      "title":{"type":"string"},"slug":{"type":"string"},"source_locale":{"type":"string"},
      "locale":{"type":"string","description":"The locale these blocks matched in, when more than one was searched."},
      "blocks":{"type":"array","description":"Matching blocks.","items":{"type":"object","additionalProperties":false,"required":["block_id"],"properties":{"block_id":{"type":"string"},"preview":{"type":"string","description":"The text around the match."}}}},
      "more_blocks":{"type":"integer","minimum":1,"description":"Further matching blocks; raise block_limit to list them."}
    }}},
    "scanned":{"type":"integer","minimum":0,"description":"Documents this caller could read in this call."},
    "unreadable":{"type":"integer","minimum":1,"description":"Documents and document types left out because reading them failed."},
    "next_offset":{"type":["integer","null"],"description":"Pass as offset to continue; null when every document was read."}
  }
}`
