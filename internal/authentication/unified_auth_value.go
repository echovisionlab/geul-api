package authentication

import "github.com/echovisionlab/geul-api/internal/structured"

// Unified-auth values represent Kratos UI JSON at the proxy boundary. They
// must be narrowed before being projected into the API-owned response shape.
type unifiedAuthValue = structured.Value
type unifiedAuthObject = structured.Fields
type unifiedAuthValues = structured.Values
