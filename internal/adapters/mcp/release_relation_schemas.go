package mcp

const releaseRelationsGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `}
}`

const releaseRelationNullableIDJSONSchema = `{"anyOf":[` + documentReferenceJSONSchema + `,{"type":"null"}]}`
const releaseRelationSortOrderJSONSchema = `{"type":"integer","minimum":-2147483648,"maximum":2147483647,"description":"Native stored sort order. Use order_intent to reorder existing artists, labels, or credits."}`
const releaseRelationOrderIntentJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["item_id"],
  "properties":{"item_id":` + documentReferenceJSONSchema + `,"previous_item_id":` + releaseRelationNullableIDJSONSchema + `,"next_item_id":` + releaseRelationNullableIDJSONSchema + `}
}`
const releaseRelationArtistJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["artist_id"],
  "properties":{"artist_id":` + documentReferenceJSONSchema + `,"sort_order":` + releaseRelationSortOrderJSONSchema + `}
}`
const releaseRelationLabelJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["label_id"],
  "properties":{"label_id":` + documentReferenceJSONSchema + `,"catalog_number":{"type":["string","null"]},"sort_order":` + releaseRelationSortOrderJSONSchema + `}
}`
const releaseRelationFormatJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["format_id"],
  "properties":{"format_id":` + documentReferenceJSONSchema + `,"format_description":{"type":["string","null"]}}
}`
const releaseRelationCreditPropertiesJSONSchema = `
  "artist_id":` + releaseRelationNullableIDJSONSchema + `,"member_id":` + releaseRelationNullableIDJSONSchema + `,
  "credited_name":{"type":["string","null"]},"credit_role":{"type":["string","null"]},"sort_order":` + releaseRelationSortOrderJSONSchema
const releaseRelationCreditJSONSchema = `{
  "type":"object","additionalProperties":false,
  "description":"Omit id or supply null only for a new credit. Retain existing IDs and unchanged optional attributes when editing a snapshot.",
  "properties":{"id":` + releaseRelationNullableIDJSONSchema + `,` + releaseRelationCreditPropertiesJSONSchema + `}
}`
const releaseRelationObservedCreditJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["id"],
  "properties":{` + releaseRelationCreditPropertiesJSONSchema + `,"id":` + documentReferenceJSONSchema + `}
}`

const releaseRelationArtistsJSONSchema = `{"type":"array","items":` + releaseRelationArtistJSONSchema + `}`
const releaseRelationLabelsJSONSchema = `{"type":"array","items":` + releaseRelationLabelJSONSchema + `}`
const releaseRelationFormatsJSONSchema = `{"type":"array","items":` + releaseRelationFormatJSONSchema + `}`
const releaseRelationCreditsJSONSchema = `{"type":"array","items":` + releaseRelationCreditJSONSchema + `}`
const releaseRelationObservedCreditsJSONSchema = `{"type":"array","items":` + releaseRelationObservedCreditJSONSchema + `}`
const releaseRelationIDsJSONSchema = `{"type":"array","items":` + documentReferenceJSONSchema + `}`

const releaseRelationsOutputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["release_id","artists","labels","category_ids","genre_ids","style_ids","formats","credits"],
  "properties":{
    "release_id":` + documentReferenceJSONSchema + `,"artists":` + releaseRelationArtistsJSONSchema + `,"labels":` + releaseRelationLabelsJSONSchema + `,
    "category_ids":` + releaseRelationIDsJSONSchema + `,"genre_ids":` + releaseRelationIDsJSONSchema + `,"style_ids":` + releaseRelationIDsJSONSchema + `,
    "formats":` + releaseRelationFormatsJSONSchema + `,"credits":` + releaseRelationObservedCreditsJSONSchema + `
  }
}`

const releaseArtistsSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","artists","observed_artists"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"artists":` + releaseRelationArtistsJSONSchema + `,"observed_artists":` + releaseRelationArtistsJSONSchema + `,"order_intent":` + releaseRelationOrderIntentJSONSchema + `}
}`
const releaseLabelsSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","labels","observed_labels"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"labels":` + releaseRelationLabelsJSONSchema + `,"observed_labels":` + releaseRelationLabelsJSONSchema + `,"order_intent":` + releaseRelationOrderIntentJSONSchema + `}
}`
const releaseCategoriesSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","category_ids","observed_category_ids"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"category_ids":` + releaseRelationIDsJSONSchema + `,"observed_category_ids":` + releaseRelationIDsJSONSchema + `}
}`
const releaseGenresSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","genre_ids","observed_genre_ids"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"genre_ids":` + releaseRelationIDsJSONSchema + `,"observed_genre_ids":` + releaseRelationIDsJSONSchema + `}
}`
const releaseStylesSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","style_ids","observed_style_ids"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"style_ids":` + releaseRelationIDsJSONSchema + `,"observed_style_ids":` + releaseRelationIDsJSONSchema + `}
}`
const releaseFormatsSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","formats","observed_formats"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"formats":` + releaseRelationFormatsJSONSchema + `,"observed_formats":` + releaseRelationFormatsJSONSchema + `}
}`
const releaseCreditsSetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","credits","observed_credits"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"credits":` + releaseRelationCreditsJSONSchema + `,"observed_credits":` + releaseRelationObservedCreditsJSONSchema + `,"order_intent":` + releaseRelationOrderIntentJSONSchema + `}
}`

const releaseRelationMutationOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["release_id","success","next_step"],
  "properties":{"release_id":` + documentReferenceJSONSchema + `,"success":{"type":"boolean","description":"Exact native SuccessResponse value; this does not report whether the relation changed."},"next_step":{"type":"string","description":"Separate release_relations_get read recipe for a fresh canonical observed snapshot."}}
}`
