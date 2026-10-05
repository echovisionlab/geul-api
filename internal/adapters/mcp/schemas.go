package mcp

// uuidJSONSchema describes an entity selector without document-specific
// discovery instructions. Individual tool fields describe the owning catalog.
const uuidJSONSchema = `{"type":"string","format":"uuid","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"}`
