package mcp

const oauthAppLifespanJSONSchema = `{"type":"string","enum":["5m","15m","1h"]}`

const oauthAppSettingsJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["name","access","redirect_uris","auth_method","access_token_lifespan","id_token_lifespan"],
  "properties":{
    "name":{"type":"string","minLength":1,"maxLength":100},
    "access":{"type":"string","enum":["none","admins","members","member_criteria"],"description":"Who may sign in: nobody, active Admins, every active Member, or Members matched by member_criteria_ids."},
    "member_criteria_ids":{"type":"array","maxItems":20,"uniqueItems":true,"items":` + uuidJSONSchema + `,"description":"Required for member_criteria, empty otherwise. IDs from member_criteria_list."},
    "redirect_uris":{"type":"array","minItems":1,"maxItems":10,"uniqueItems":true,"items":{"type":"string"},"description":"Absolute https URIs; http only for localhost."},
    "post_logout_redirect_uris":{"type":"array","maxItems":10,"uniqueItems":true,"items":{"type":"string"}},
    "scopes":{"type":"array","uniqueItems":true,"items":{"type":"string","enum":["email","profile"]},"description":"Profile data besides the account ID, which every app receives."},
    "auth_method":{"type":"string","enum":["client_secret_basic","client_secret_post","none"],"description":"none is a public app that must use PKCE."},
    "access_token_lifespan":` + oauthAppLifespanJSONSchema + `,
    "id_token_lifespan":` + oauthAppLifespanJSONSchema + `
  }
}`

const oauthAppJSONSchema = `{
  "type":"object","additionalProperties":false,
  "required":["client_id","revision","name","access","member_criteria","redirect_uris","post_logout_redirect_uris","scopes","auth_method","access_token_lifespan","id_token_lifespan","created_at"],
  "properties":{
    "client_id":{"type":"string"},"revision":{"type":"string"},"name":{"type":"string"},
    "access":{"type":"string"},
    "member_criteria":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","kind","eligible"],"properties":{"id":` + uuidJSONSchema + `,"name":{"type":"string"},"kind":{"type":"string"},"eligible":{"type":"boolean","description":"False for an archived criteria, which admits nobody."}}}},
    "redirect_uris":{"type":"array","items":{"type":"string"}},
    "post_logout_redirect_uris":{"type":"array","items":{"type":"string"}},
    "scopes":{"type":"array","items":{"type":"string"}},
    "auth_method":{"type":"string"},"access_token_lifespan":{"type":"string"},"id_token_lifespan":{"type":"string"},
    "created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"}
  }
}`

const oauthAppEmptyInputJSONSchema = `{"type":"object","additionalProperties":false,"properties":{}}`

const oauthAppClientIDInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id"],
  "properties":{"client_id":{"type":"string","minLength":1}}
}`

const oauthAppRevisionInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id","expected_revision"],
  "properties":{"client_id":{"type":"string","minLength":1},"expected_revision":{"type":"string","minLength":1,"description":"revision from the latest oauth_app_get or write."}}
}`

const oauthAppListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["apps"],
  "properties":{"apps":{"type":"array","items":` + oauthAppJSONSchema + `}}
}`

const oauthAppOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["app"],
  "properties":{"app":` + oauthAppJSONSchema + `}
}`

const oauthAppSecretOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["app"],
  "properties":{"app":` + oauthAppJSONSchema + `,"client_secret":{"type":"string","description":"Shown only in this result; it cannot be read again."}}
}`

const oauthAppCreateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["settings"],
  "properties":{
    "client_id":{"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$","description":"Optional; omit to generate one. It cannot change later."},
    "settings":` + oauthAppSettingsJSONSchema + `
  }
}`

const oauthAppUpdateInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id","expected_revision","settings"],
  "properties":{
    "client_id":{"type":"string","minLength":1},
    "expected_revision":{"type":"string","minLength":1,"description":"revision from the latest oauth_app_get or write."},
    "settings":` + oauthAppSettingsJSONSchema + `
  }
}`

const oauthAppDeletedOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id","deleted"],
  "properties":{"client_id":{"type":"string"},"deleted":{"type":"boolean"}}
}`

const oauthConnectedToolListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["tools"],
  "properties":{"tools":{"type":"array","items":{"type":"object","additionalProperties":false,
    "required":["client_id","name","stale","redirect_uris","resource","auth_method"],
    "properties":{
      "client_id":{"type":"string"},"name":{"type":"string"},
      "publisher":{"type":"string","description":"Host that published the tool's registration; absent for a generated ID."},
      "stale":{"type":"boolean","description":"The tool targets an address the site no longer serves."},
      "redirect_uris":{"type":"array","items":{"type":"string"}},"resource":{"type":"string"},"auth_method":{"type":"string"},
      "created_at":{"type":"string","format":"date-time"}
    }}}}
}`

const oauthConnectedToolDisconnectOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["client_id","disconnected"],
  "properties":{"client_id":{"type":"string"},"disconnected":{"type":"boolean"}}
}`

const memberCriteriaListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string","description":"Search criteria names."},"limit":{"type":"integer","minimum":1,"maximum":100,"default":50},"offset":{"type":"integer","minimum":0,"maximum":2147483647,"default":0}}
}`

const memberCriteriaListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total","limit","offset","has_more"],
  "properties":{
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name","kind"],"properties":{"id":` + uuidJSONSchema + `,"name":{"type":"string"},"description":{"type":"string"},"kind":{"type":"string"}}}},
    "total":{"type":"integer"},"limit":{"type":"integer"},"offset":{"type":"integer"},"has_more":{"type":"boolean"},"next_offset":{"type":"integer"}
  }
}`
