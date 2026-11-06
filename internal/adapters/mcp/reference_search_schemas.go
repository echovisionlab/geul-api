package mcp

const referenceLookupInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type"],
  "properties":{
    "reference_type":{"enum":["category","tag","client","map_place","member","artist","genre","style","format","program_event_type","program_event_series","member_tag"]},
    "query":{"type":"string","description":"Search names. Required for category, tag, client, map_place, member and artist; the others list everything without it."},
    "limit":{"type":"integer","minimum":1,"maximum":100,"default":20,"description":"Maximum results. At most 50 for client and map_place."},
    "offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0,"description":"next_offset from the previous call. Not for category, tag, client, map_place, member and artist, which refine by query."},
    "status":{"type":"string","description":"program_event_type: PROGRAM_EVENT_TYPE_STATUS_ACTIVE or _INACTIVE. program_event_series: PROGRAM_EVENT_SERIES_STATUS_DRAFT or _PUBLISHED."}
  }
}`

const referenceLookupOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type","items","count","has_more"],
  "properties":{
    "reference_type":{"type":"string"},
    "count":{"type":"integer"},
    "total":{"type":"integer","description":"Every match, for the types that list with an offset."},
    "has_more":{"type":["boolean","null"],"description":"True when more matches exist. Null means the quick-search service cap was reached and completeness is unknown; refine the query."},
    "next_offset":{"type":"integer","description":"Pass as offset with the same arguments to continue."},
    "items":{"type":"array","items":{"type":"object","required":["id"],"description":"id is the UUID to pass where this reference is used, never a slug or URL. The other fields differ by type: name or title, slug, status, and so on.","properties":{"id":{"type":"string"}}}}
  }
}`
