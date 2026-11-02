package page

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func TestCreateSourceLocaleHonorsExplicitCanonicalLocaleAndRetainsFallback(t *testing.T) {
	store := newMemoryStore()
	preferred := "ko"
	store.preferred = &preferred
	memberID := "11111111-1111-4111-8111-111111111111"
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(memberID)})
	// The explicit locale must win even when the member prefers ko and the
	// header requests ja.
	locale, err := resolveCreateSourceLocale(ctx, store, "en", "ja")
	if err != nil || locale != "en" {
		t.Fatalf("explicit locale=(%q, %v)", locale, err)
	}
	locale, err = resolveCreateSourceLocale(ctx, store, "", "ja")
	if err != nil || locale != "ko" {
		t.Fatalf("omitted locale lost preference fallback=(%q, %v)", locale, err)
	}
	locale, err = resolveCreateSourceLocale(t.Context(), store, "", "ja")
	if err != nil || locale != "ja" {
		t.Fatalf("anonymous header fallback=(%q, %v)", locale, err)
	}
	locale, err = resolveCreateSourceLocale(t.Context(), store, "", "")
	if err != nil || locale != store.settings.DefaultLocale {
		t.Fatalf("settings fallback=(%q, %v)", locale, err)
	}
	for _, requested := range []string{"xx", "EN", "en-US", " en "} {
		if _, err := resolveCreateSourceLocale(ctx, store, requested, "ja"); errs.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("unsupported locale %q error=%v", requested, err)
		}
	}
}

func TestNormalizePageDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizePageDocumentLocale("ko"); err != nil || locale != "ko" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizePageDocumentLocale(locale); errs.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizePageDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
