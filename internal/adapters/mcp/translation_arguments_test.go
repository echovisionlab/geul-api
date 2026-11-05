package mcp

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestTranslationLocaleTagShapeRejectsEmptySubtags(t *testing.T) {
	for _, locale := range []core.Locale{"en", "zh-CN", "es-419", "pt-PT"} {
		if err := validateCompactLocale(locale); err != nil {
			t.Fatalf("supported tag shape %q rejected: %v", locale, err)
		}
	}
	for _, locale := range []core.Locale{"en--US", "-en", "en-", "en_US", "en US", ""} {
		if err := validateCompactLocale(locale); err == nil {
			t.Fatalf("invalid tag shape %q accepted", locale)
		}
	}
}
