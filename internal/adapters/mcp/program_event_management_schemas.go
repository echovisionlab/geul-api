package mcp

const programEventArtistJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["artist_id"],
  "properties":{"artist_id":` + documentReferenceJSONSchema + `,"role":{"type":"string"},"sort_order":{"type":"integer"}}
}`

const programEventLabelJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["label_id"],
  "properties":{"label_id":` + documentReferenceJSONSchema + `,"role":{"type":"string"},"sort_order":{"type":"integer"}}
}`

const programEventClientJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id"],
  "properties":{"client_id":` + documentReferenceJSONSchema + `,"role":{"type":"string"},"sort_order":{"type":"integer"}}
}`

const programEventCreditJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{
    "id":` + documentReferenceJSONSchema + `,
    "artist_id":` + documentReferenceJSONSchema + `,"member_id":` + documentReferenceJSONSchema + `,
    "display_name":{"type":"string"},"credit_role":{"type":"string"},"description":{"type":"string"},"sort_order":{"type":"integer"}
  }
}`

const programEventArtistsJSONSchema = `{"type":"array","maxItems":256,"items":` + programEventArtistJSONSchema + `}`
const programEventLabelsJSONSchema = `{"type":"array","maxItems":256,"items":` + programEventLabelJSONSchema + `}`
const programEventClientsJSONSchema = `{"type":"array","maxItems":256,"items":` + programEventClientJSONSchema + `}`
const programEventCreditsJSONSchema = `{"type":"array","maxItems":256,"items":` + programEventCreditJSONSchema + `}`

const programEventLocationModeJSONSchema = `{"enum":["map_place","online","hybrid","tba"],"description":"Explicit event location. map_place and hybrid require a Map Place UUID; online and tba do not use a Map Place."}`

const programEventCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["title","slug","source_locale","type_id","starts_at","timezone","location_mode"],
  "properties":{
    "title":{"type":"string","minLength":1},
    "slug":{"type":"string","minLength":1,"maxLength":160,"description":"Unique event route segment using lowercase letters, digits, and hyphens; no slash."},
    "source_locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "type_id":` + documentReferenceJSONSchema + `,
    "starts_at":{"type":"string","format":"date-time","description":"RFC 3339 instant with an explicit UTC offset, such as 2026-10-10T19:00:00+09:00."},
    "ends_at":{"type":"string","format":"date-time","description":"Optional RFC 3339 end instant, at or after starts_at."},
    "timezone":{"type":"string","minLength":1,"maxLength":100,"description":"IANA timezone for local event display, such as Asia/Seoul or UTC. The timestamp offset specifies the instant independently."},
    "all_day":{"type":"boolean","default":false},"location_mode":` + programEventLocationModeJSONSchema + `,
    "map_place_id":` + documentReferenceJSONSchema + `,
    "summary":{"type":"string"},"series_id":` + documentReferenceJSONSchema + `,"series_order":{"type":"integer"},
    "poster_file_id":` + documentReferenceJSONSchema + `,
    "ticket_url":{"type":"string"},"stream_url":{"type":"string"},"external_url":{"type":"string"},
    "artists":` + programEventArtistsJSONSchema + `,"labels":` + programEventLabelsJSONSchema + `,
    "clients":` + programEventClientsJSONSchema + `,"credits":` + programEventCreditsJSONSchema + `
  },
  "allOf":[{"if":{"properties":{"location_mode":{"enum":["map_place","hybrid"]}},"required":["location_mode"]},"then":{"required":["map_place_id"]}}]
}`

const programEventSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "slug":{"type":"string","minLength":1,"maxLength":160},"type_id":` + documentReferenceJSONSchema + `,
    "starts_at":{"type":"string","format":"date-time","description":"RFC 3339 instant with an explicit UTC offset."},
    "ends_at":{"type":"string","format":"date-time"},
    "clear_ends_at":{"type":"boolean","default":false,"description":"Remove the end instant. When true, takes precedence over ends_at."},
    "timezone":{"type":"string","minLength":1,"maxLength":100,"description":"IANA timezone, such as Asia/Seoul or UTC."},
    "all_day":{"type":"boolean"},"location_mode":` + programEventLocationModeJSONSchema + `,
    "map_place_id":{"type":"string","description":"Canonical Map Place UUID. An empty string clears it only if the resulting location mode permits no place. Switching to online or tba clears it automatically."},
    "series_id":{"type":"string","description":"Canonical Series UUID from program_event_series_list, or an empty string to remove the relation."},
    "series_order":{"type":"integer"},"clear_series_order":{"type":"boolean","default":false,"description":"Remove series_order. When true, takes precedence over series_order."},
    "poster_file_id":{"type":"string","description":"Canonical File UUID, or an empty string to remove the primary poster relation."},
    "ticket_url":{"type":"string","description":"An empty string removes the URL."},
    "stream_url":{"type":"string","description":"An empty string removes the URL."},
    "external_url":{"type":"string","description":"An empty string removes the URL."},
    "artists":` + programEventArtistsJSONSchema + `,"observed_artists":` + programEventArtistsJSONSchema + `,
    "labels":` + programEventLabelsJSONSchema + `,"observed_labels":` + programEventLabelsJSONSchema + `,
    "clients":` + programEventClientsJSONSchema + `,"observed_clients":` + programEventClientsJSONSchema + `,
    "credits":` + programEventCreditsJSONSchema + `
  },
  "allOf":[
    {"if":{"required":["artists"]},"then":{"required":["observed_artists"]}},
    {"if":{"required":["labels"]},"then":{"required":["observed_labels"]}},
    {"if":{"required":["clients"]},"then":{"required":["observed_clients"]}}
  ]
}`

const programEventSettingsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","title","slug","status","source_locale","type_id","starts_at","timezone","all_day","location_mode","document_revision","updated_at","artists","labels","clients","credits","locales"],
  "properties":{
    "document_type":{"const":"program_event"},"document_id":` + documentReferenceJSONSchema + `,"changed":{"type":"boolean"},
    "title":{"type":"string"},"slug":{"type":"string"},"status":{"enum":["draft","published","archived"]},"source_locale":{"type":"string"},
    "type_id":` + documentReferenceJSONSchema + `,"series_id":` + documentReferenceJSONSchema + `,"series_order":{"type":"integer"},
    "starts_at":{"type":"string","format":"date-time"},"ends_at":{"type":"string","format":"date-time"},"timezone":{"type":"string"},"all_day":{"type":"boolean"},
    "location_mode":` + programEventLocationModeJSONSchema + `,"map_place_id":` + documentReferenceJSONSchema + `,"poster_file_id":` + documentReferenceJSONSchema + `,
    "ticket_url":{"type":"string"},"stream_url":{"type":"string"},"external_url":{"type":"string"},
    "document_revision":{"type":"string","description":"Current content document revision. Settings updates use native event rules; relation updates require observed arrays rather than this revision."},
    "updated_at":{"type":"string","format":"date-time"},"published_at":{"type":"string","format":"date-time"},
    "artists":{"type":"array","items":` + programEventArtistJSONSchema + `},
    "labels":{"type":"array","items":` + programEventLabelJSONSchema + `},
    "clients":{"type":"array","items":` + programEventClientJSONSchema + `},
    "credits":{"type":"array","items":` + programEventCreditJSONSchema + `},
    "locales":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["locale"],"properties":{"locale":{"type":"string"},"summary":{"type":"string"}}}}
  }
}`

const programEventMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","changed"],
  "properties":{
    "document_type":{"const":"program_event"},"document_id":` + documentReferenceJSONSchema + `,
    "changed":{"type":"boolean"},"deleted":{"type":"boolean"},"status":{"enum":["draft","published","archived"]},
    "updated_at":{"type":"string","format":"date-time"},"published_at":{"type":"string","format":"date-time"}
  }
}`
