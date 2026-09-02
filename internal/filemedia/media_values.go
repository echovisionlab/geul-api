package filemedia

import (
	"strings"
)

func canonicalMimeType(value string) string {
	return strings.TrimSpace(strings.Split(value, ";")[0])
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}
