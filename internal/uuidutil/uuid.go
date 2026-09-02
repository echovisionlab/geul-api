package uuidutil

import (
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ParseCanonical accepts only the lowercase hyphenated UUID representation.
// It intentionally rejects whitespace, braces, URNs, and uppercase spellings
// even when uuid.Parse would otherwise accept them.
func ParseCanonical(value, field string) (uuid.UUID, error) {
	if !canonicalUUIDPattern.MatchString(value) {
		return uuid.Nil, fmt.Errorf("%s must be a canonical UUID", field)
	}
	parsed, err := uuid.Parse(value)
	if err != nil || value != parsed.String() {
		return uuid.Nil, fmt.Errorf("%s must be a canonical UUID", field)
	}
	return parsed, nil
}
