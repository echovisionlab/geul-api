// Package structured names the deliberately heterogeneous values used at
// JSON, SQL-driver, logging, and third-party API boundaries.
package structured

// Value is a boundary value whose concrete type is determined by the owning
// encoder, decoder, or external API.
type Value = interface{}

// Fields is a string-keyed structured record.
type Fields = map[string]interface{}

// Values is an ordered collection of structured values.
type Values = []interface{}
