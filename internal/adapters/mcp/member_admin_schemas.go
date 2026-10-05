package mcp

const memberAdminListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string","description":"Search nickname or email."},"status":{"enum":["active","banned","pending_deletion","deleted"]},"limit":{"type":"integer","minimum":1,"maximum":100,"default":20},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}}
}`

const memberAdminGetInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["member_id"],
  "properties":{"member_id":` + documentReferenceJSONSchema + `}
}`

const memberAdminOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["profile","account","tag_ids","onboarded","newsletter_subscription"],
  "properties":{
    "profile":{"type":"object","additionalProperties":false,"required":["id","nickname","deleted"],"properties":{
      "id":` + documentReferenceJSONSchema + `,"nickname":{"type":"string"},"deleted":{"type":"boolean"},"avatar_asset_id":` + documentReferenceJSONSchema + `,
      "bio":{"type":"string"},"website":{"type":"string"},"social_links":{"type":"object","additionalProperties":{"type":"string"}},"preferred_locale":{"type":"string"},
      "created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
    }},
    "account":{"type":["object","null"],"additionalProperties":false,"required":["role","status","banned"],"properties":{
      "canonical_email":{"type":"object","additionalProperties":false,"required":["email","verified"],"properties":{"email":{"type":"string"},"verified":{"type":"boolean"}}},
      "role":{"enum":["unspecified","anon","user","author","admin"]},"status":{"enum":["unspecified","active","banned","pending_deletion","deleted"]},"banned":{"type":"boolean"},
      "ban_details":{"type":"object","additionalProperties":false,"required":["metadata_banned","identity_state","inactive_state"],"properties":{"metadata_banned":{"type":"boolean"},"identity_state":{"type":"string"},"inactive_state":{"type":"boolean"},"reason":{"type":"string"},"expires_at":{"type":"string","format":"date-time"}}}
    }},
    "tag_ids":{"type":"array","items":` + documentReferenceJSONSchema + `},"onboarded":{"type":"boolean"},
    "newsletter_subscription":{"type":"object","additionalProperties":false,"required":["subscribed"],"properties":{"subscribed":{"type":"boolean"},"subscribed_at":{"type":"string","format":"date-time"}}}
  }
}`

const memberAdminListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["members","total","limit","offset","has_more"],
  "properties":{"members":{"type":"array","items":` + memberAdminOutputJSONSchema + `},"total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"}}
}`

const memberAdminGetOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["member"],
  "properties":{"member":` + memberAdminOutputJSONSchema + `}
}`
