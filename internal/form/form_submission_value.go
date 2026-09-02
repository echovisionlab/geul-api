package form

import "github.com/echovisionlab/geul-api/internal/structured"

// Submission values mirror user-authored form JSON. Validation is responsible
// for narrowing each value according to its owning field schema.
type submissionValue = structured.Value
type submissionObject = structured.Fields
type submissionValues = structured.Values
