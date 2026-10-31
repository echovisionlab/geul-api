// Package optional copies and compares optional values held as pointers, such
// as a target revision that is absent until the target locale exists.
package optional

// Clone returns a pointer to a copy of *value, or nil.
func Clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// Equal reports whether both values are absent or both hold equal values.
func Equal[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
