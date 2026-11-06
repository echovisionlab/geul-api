package mcp

const menuLocationsInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{}}`
const menuLocationIDJSONSchema = `{"type":["string","null"],"format":"uuid","description":"Assigned Menu UUID, or null when unassigned. Read with document_read using document_type=menu."}`
const menuLocationsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["menu_header_id","menu_secondary_id","menu_footer_id","menu_avatar_dropdown_id"],
  "properties":{
    "menu_header_id":` + menuLocationIDJSONSchema + `,
    "menu_secondary_id":` + menuLocationIDJSONSchema + `,
    "menu_footer_id":` + menuLocationIDJSONSchema + `,
    "menu_avatar_dropdown_id":` + menuLocationIDJSONSchema + `
  }
}`
