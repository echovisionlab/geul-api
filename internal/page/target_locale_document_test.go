package page

import (
	"testing"

	"connectrpc.com/connect"
)

func TestNormalizePageDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizePageDocumentLocale("ko"); err != nil || locale != "ko" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizePageDocumentLocale(locale); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizePageDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
