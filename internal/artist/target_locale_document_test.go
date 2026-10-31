package artist

import (
	"testing"

	"connectrpc.com/connect"
)

func TestNormalizeArtistDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizeArtistDocumentLocale("ko"); err != nil || locale != "ko" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizeArtistDocumentLocale(locale); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizeArtistDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
