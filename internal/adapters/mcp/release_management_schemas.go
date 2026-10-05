package mcp

const releaseTypeJSONSchema = `{"enum":["album","ep","single","compilation"]}`

const releaseCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["title","source_locale","type"],
  "properties":{
    "title":{"type":"string","minLength":1},
    "source_locale":{"type":"string","minLength":1,"maxLength":35,"pattern":"^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$"},
    "type":` + releaseTypeJSONSchema + `,
    "slug":{"type":"string","description":"Optional route slug without a slash. Check availability with release_slug_check."},
    "catalog_number":{"type":"string"},
    "release_date":{"type":"string","format":"date-time","description":"Optional RFC 3339 release instant with an explicit offset, such as 2026-10-10T00:00:00+09:00."},
    "spotify_url":{"type":"string"},"apple_music_url":{"type":"string"},"bandcamp_url":{"type":"string"},"youtube_music_url":{"type":"string"}
  }
}`

const releaseSettingsUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id"],
  "properties":{
    "document_id":` + documentReferenceJSONSchema + `,
    "slug":{"type":"string","description":"Optional new route slug without a slash. Omit to keep the current value; an empty string stores an empty value."},
    "type":` + releaseTypeJSONSchema + `,
    "catalog_number":{"type":"string","description":"Omit to keep the current value; an empty string stores an empty value."},
    "release_date":{"type":"string","format":"date-time","description":"RFC 3339 instant with an explicit offset. Omit clear_release_date or set it false when supplying this field."},
    "clear_release_date":{"type":"boolean","default":false,"description":"Remove the release date through the native clear-release-date oneof. release_date must be omitted when true."},
    "spotify_url":{"type":"string"},"apple_music_url":{"type":"string"},"bandcamp_url":{"type":"string"},"youtube_music_url":{"type":"string"}
  },
  "allOf":[{"if":{"required":["clear_release_date"],"properties":{"clear_release_date":{"const":true}}},"then":{"not":{"required":["release_date"]}}}]
}`

const releaseSettingsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["document_type","document_id","title","source_locale","type","status","document_revision","updated_at"],
  "properties":{
    "document_type":{"const":"release"},"document_id":` + documentReferenceJSONSchema + `,"changed":{"type":"boolean"},
    "title":{"type":"string"},"slug":{"type":"string"},"source_locale":{"type":"string"},"type":` + releaseTypeJSONSchema + `,
    "status":{"enum":["draft","published"]},
    "document_revision":{"type":"string","description":"Content document revision for DCDP editing. It is not a root-settings CAS token."},
    "catalog_number":{"type":"string"},"release_date":{"type":"string","format":"date-time"},
    "spotify_url":{"type":"string"},"apple_music_url":{"type":"string"},"bandcamp_url":{"type":"string"},"youtube_music_url":{"type":"string"},
    "artwork_asset_id":` + documentReferenceJSONSchema + `,"og_asset_id":` + documentReferenceJSONSchema + `,
    "published_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
  }
}`

const releaseMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","changed"],
  "properties":{
    "document_type":{"const":"release"},"document_id":` + documentReferenceJSONSchema + `,
    "changed":{"type":"boolean"},"deleted":{"type":"boolean"},"status":{"enum":["draft","published"]},
    "release_date":{"type":"string","format":"date-time"},"published_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
  }
}`

const releaseArtworkSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_id","file_id"],
  "properties":{"document_id":` + documentReferenceJSONSchema + `,"file_id":` + documentReferenceJSONSchema + `}
}`

const releaseArtworkOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","document_id","success"],
  "properties":{
    "document_type":{"const":"release"},"document_id":` + documentReferenceJSONSchema + `,"file_id":` + documentReferenceJSONSchema + `,
    "success":{"type":"boolean","description":"Native operation completed; this does not distinguish a mutation from a no-op."},
    "artwork_asset_id":` + documentReferenceJSONSchema + `,"og_generation_run_id":{"type":"string"}
  }
}`

const releaseSlugCheckInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["slug"],
  "properties":{"slug":{"type":"string","description":"Release route slug without a slash."},"exclude_document_id":` + documentReferenceJSONSchema + `}
}`

const releaseSlugCheckOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["document_type","slug","available"],
  "properties":{"document_type":{"const":"release"},"slug":{"type":"string"},"available":{"type":"boolean"}}
}`
