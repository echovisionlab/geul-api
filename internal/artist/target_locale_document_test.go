package artist

import (
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func TestNormalizeArtistDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizeArtistDocumentLocale("ko"); err != nil || locale != "ko" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizeArtistDocumentLocale(locale); errs.Wrap(err).Code() != connect.CodeInvalidArgument {
			t.Fatalf("normalizeArtistDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
