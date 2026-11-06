package mcp

const textEditLocationProperties = `"document_type":` + domainJSONSchema + `,
      "document_id":` + documentReferenceJSONSchema + `,
      "locale":` + plainLocaleJSONSchema + `,
      "block_id":{"type":"string","minLength":1,"maxLength":160,"description":"Limit the edit to this Block."},
      "all_occurrences":{"type":"boolean","default":false,"description":"Change every match instead of requiring exactly one."}`

const textEditDryRunProperty = `"dry_run":{"type":"boolean","default":false,"description":"Run the edit and roll it back: the result shows each change before and after, and nothing changes."}`

const textReplaceInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["edits"],
  "properties":{` + textEditDryRunProperty + `,"edits":{"type":"array","minItems":1,"maxItems":500,"description":"Edits in order; a later edit of a document sees the text the earlier ones left.","items":{
    "type":"object","additionalProperties":false,"required":["document_type","document_id","locale","find","replace"],
    "properties":{` + textEditLocationProperties + `,
      "find":{"type":"string","minLength":1,"maxLength":2000,"description":"Exact text to replace; must sit inside one formatted run."},
      "replace":{"type":"string","maxLength":20000,"description":"Replacement text; empty deletes find. It takes the formatting of the run it replaces."},
      "translations":{"type":"object","minProperties":1,"maxProperties":64,"description":"With a source-locale edit: the whole translated text per locale of the translation unit holding replace, written in the same transaction. Plain text for a one-run unit; otherwise XLIFF 2.2 inline markup.","additionalProperties":{"type":"string","maxLength":100000}}
    }
  }}}
}`

const textFormatInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["edits"],
  "properties":{` + textEditDryRunProperty + `,"edits":{"type":"array","minItems":1,"maxItems":500,"description":"Edits in order; a later edit of a document sees what the earlier ones left.","items":{
    "type":"object","additionalProperties":false,"required":["document_type","document_id","locale"],
    "properties":{` + textEditLocationProperties + `,
      "find":{"type":"string","minLength":1,"maxLength":2000,"description":"Exact text to format; it may span existing marks but not a line break. Pass this or find_link."},
      "find_link":{"type":"string","minLength":1,"maxLength":2048,"description":"Exact link address whose linked text to format. Pass this or find."},
      "link":{"type":"string","maxLength":2048,"description":"New link address; empty removes the link."},
      "add_marks":{"type":"array","uniqueItems":true,"items":{"enum":["bold","italic","underline","strike","code"]}},
      "remove_marks":{"type":"array","uniqueItems":true,"items":{"enum":["bold","italic","underline","strike","code"]}},
      "text_color":{"type":"string","maxLength":64,"description":"New text color; empty removes it."},
      "background_color":{"type":"string","maxLength":64,"description":"New background color; empty removes it."}
    }
  }}}
}`

const textEditOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["documents","applied","failed"],
  "properties":{
    "documents":{"type":"array","description":"One entry per document, in the order first edited.","items":{
      "type":"object","additionalProperties":false,"required":["document_type","document_id","status"],
      "properties":{
        "document_type":{"type":"string"},
        "document_id":{"type":"string"},
        "status":{"enum":["applied","would_apply","failed"],"description":"would_apply is a dry_run that would succeed. failed means nothing changed in this document."},
        "error":{"type":"string","description":"Why the document failed."},
        "edit_index":{"type":"integer","minimum":0,"description":"The edit in this call that failed, when one edit is to blame."},
        "locales":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["locale","blocks"],"properties":{
          "locale":{"type":"string"},
          "document_revision":{"type":"string","description":"Left out for a dry_run, whose writes rolled back."},
          "target_revision":{"type":"string","description":"Left out for a dry_run, and when this edit also wrote the source, which moves it."},
          "blocks":{"type":"array","description":"The Blocks changed in this locale.","items":{"type":"string"}},
          "changes":{"type":"array","description":"dry_run only: each changed field before and after, around the first difference.","items":{"type":"object","additionalProperties":false,"required":["block_id","field","before","after"],"properties":{
            "block_id":{"type":"string"},"field":{"type":"string"},"before":{"type":"string"},"after":{"type":"string"}
          }}}
        }}},
        "translations":{"type":"object","description":"The translations written with the source edit, per locale.","additionalProperties":{"type":"object","additionalProperties":false,"required":["changed"],"properties":{
          "target_revision":{"type":"string","description":"Left out for a dry_run."},"changed":{"type":"boolean"}
        }}}
      }
    }},
    "applied":{"type":"integer","minimum":0,"description":"Documents fully applied, or that would be under dry_run."},
    "failed":{"type":"integer","minimum":0,"description":"Documents left unchanged."}
  }
}`
