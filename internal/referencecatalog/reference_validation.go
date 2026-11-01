package referencecatalog

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/pathslug"
)

// Reference kinds bound name and slug by their column length: varchar(100) for
// genre, style and category, varchar(50) for format and tag.
const (
	referenceValueMaxLength      = 100
	shortReferenceValueMaxLength = 50
)

func normalizeReferenceName(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domainerr.Required("name")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", domainerr.InvalidField("name", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	return value, nil
}

func normalizeReferenceSlug(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domainerr.Required("slug")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", domainerr.InvalidField("slug", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	if err := pathslug.Validate(value); err != nil {
		return "", err
	}
	return value, nil
}
