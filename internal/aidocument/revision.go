package aidocument

// OptionalRevision converts a domain's optional revision to the protocol
// type; nil stays nil.
func OptionalRevision(value *string) *Revision {
	if value == nil {
		return nil
	}
	revision := Revision(*value)
	return &revision
}

// OptionalRevisionString converts an optional protocol revision back to the
// domain's string form; nil stays nil.
func OptionalRevisionString(value *Revision) *string {
	if value == nil {
		return nil
	}
	revision := string(*value)
	return &revision
}
