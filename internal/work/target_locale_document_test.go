package work

import (
	"testing"

	"connectrpc.com/connect"
)

func TestNormalizeWorkDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizeWorkDocumentLocale("zh-TW"); err != nil || locale != "zh-TW" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizeWorkDocumentLocale(locale); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizeWorkDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
