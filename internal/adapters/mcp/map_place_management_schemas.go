package mcp

const mapPlaceAddressComponentsJSONSchema = `{"type":"object","additionalProperties":false,"properties":{"street":{"type":"string"},"city":{"type":"string"},"region":{"type":"string"},"country":{"type":"string"},"postal_code":{"type":"string"}}}`

const mapPlaceEditableProperties = `"name":{"type":"string","minLength":1},"address":{"type":"string","minLength":1},"lat":{"type":"number","minimum":-90,"maximum":90},"lng":{"type":"number","minimum":-180,"maximum":180},"address_components":` + mapPlaceAddressComponentsJSONSchema + `,"image_file_id":` + documentReferenceJSONSchema + `,"google_place_id":{"type":"string","description":"Google Places identifier; an empty string clears the link during update."}`

const mapPlaceIDProperties = `"map_place_id":` + documentReferenceJSONSchema
const mapPlaceGetInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["map_place_id"],"properties":{` + mapPlaceIDProperties + `}}`
const mapPlaceGetManyInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["map_place_ids"],"properties":{"map_place_ids":{"type":"array","minItems":1,"maxItems":100,"items":` + documentReferenceJSONSchema + `}}}`
const mapPlaceListInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{` + programEventReferencePaginationProperties + `,"query":{"type":"string","description":"Search place name or address; omitted or empty lists all places."}}}`
const mapPlaceCreateInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["name","address","lat","lng"],"properties":{` + mapPlaceEditableProperties + `}}`
const mapPlaceSettingsUpdateInputJSONSchema = `{"type":"object","additionalProperties":false,"required":["map_place_id"],"properties":{` + mapPlaceIDProperties + `,` + mapPlaceEditableProperties + `,"clear_image":{"type":"boolean","description":"Remove the current image; takes precedence over image_file_id when both are supplied."}}}`

const mapPlaceProjectionJSONSchema = `{"type":"object","additionalProperties":false,"required":["id","name","address","lat","lng"],"properties":{"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"},"address":{"type":"string"},"lat":{"type":"number"},"lng":{"type":"number"},"address_components":` + mapPlaceAddressComponentsJSONSchema + `,"image_file_id":` + documentReferenceJSONSchema + `,"image_url":{"type":"string"},"google_place_id":{"type":"string"}}}`
const mapPlaceGetOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["place"],"properties":{"place":` + mapPlaceProjectionJSONSchema + `}}`
const mapPlaceGetManyOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":` + mapPlaceProjectionJSONSchema + `}}}`
const mapPlaceListOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],"properties":{"items":{"type":"array","items":` + mapPlaceProjectionJSONSchema + `},"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer","description":"Present when more matches remain; continue with this offset and the same query."}}}`
const mapPlaceDeleteOutputJSONSchema = `{"type":"object","additionalProperties":false,"required":["success"],"properties":{"success":{"type":"boolean"}}}`
