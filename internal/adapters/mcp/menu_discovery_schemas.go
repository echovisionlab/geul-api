package mcp

const menuListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{` + pageReferencePaginationProperties + `}
}`

const menuListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{` + pageReferencePaginationOutputProperties + `,
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","source_locale"],"properties":{
      "id":{"type":"string","format":"uuid","description":"Canonical Menu UUID. Pass unchanged as d with p=menu; never use an item ID, name, slug, or URL."},
      "name":{"type":"string","description":"Management name, not proof of current site placement."},
      "source_locale":{"type":"string"}
    }}}
  }
}`

const menuLocationsInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{}}`
const menuLocationIDJSONSchema = `{"type":["string","null"],"format":"uuid","description":"Assigned Menu UUID, or null when unassigned. Read with document_open using p=menu."}`
const menuLocationsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["menu_header_id","menu_secondary_id","menu_footer_id","menu_avatar_dropdown_id"],
  "properties":{
    "menu_header_id":` + menuLocationIDJSONSchema + `,
    "menu_secondary_id":` + menuLocationIDJSONSchema + `,
    "menu_footer_id":` + menuLocationIDJSONSchema + `,
    "menu_avatar_dropdown_id":` + menuLocationIDJSONSchema + `
  }
}`
