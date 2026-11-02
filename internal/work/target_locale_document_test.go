package work

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestCreateSourceLocaleHonorsExplicitCanonicalLocaleAndRetainsFallback(t *testing.T) {
	memberID := "11111111-1111-4111-8111-111111111111"
	var preferenceReads []string
	preferred := "ko"
	store := stubStore{preferredLocale: func(id string) (*string, error) {
		preferenceReads = append(preferenceReads, id)
		return &preferred, nil
	}}
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(memberID)})
	// The explicit locale needs no preference read and must win even when the
	// member prefers ko and the header requests ja.
	locale, err := resolveCreateSourceLocale(ctx, store, "en", "ja")
	if err != nil || locale != "en" || len(preferenceReads) != 0 {
		t.Fatalf("explicit locale=(%q, %v), preference reads %v", locale, err, preferenceReads)
	}
	locale, err = resolveCreateSourceLocale(ctx, store, "", "ja")
	if err != nil || locale != "ko" || len(preferenceReads) != 1 || preferenceReads[0] != memberID {
		t.Fatalf("omitted locale lost preference fallback=(%q, %v), preference reads %v", locale, err, preferenceReads)
	}
	locale, err = resolveCreateSourceLocale(t.Context(), nil, "", "ja")
	if err != nil || locale != "ja" {
		t.Fatalf("anonymous header fallback=(%q, %v)", locale, err)
	}
	for _, requested := range []string{"xx", "EN", "en-US", " en "} {
		if _, err := resolveCreateSourceLocale(ctx, store, requested, "ja"); errs.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("unsupported locale %q error=%v", requested, err)
		}
	}
}

func TestCreateSourceLocaleFallsBackToTheRuntimeDefault(t *testing.T) {
	store := stubStore{
		preferredLocale: func(string) (*string, error) { return nil, errors.New("preference unavailable") },
		defaultLocale:   func() (string, error) { return "ja", nil },
	}
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(uuid.NewString())})
	locale, err := resolveCreateSourceLocale(ctx, store, "", "")
	if err != nil || locale != "ja" {
		t.Fatalf("runtime default fallback=(%q, %v)", locale, err)
	}
	store.defaultLocale = func() (string, error) { return "", errors.New("settings unavailable") }
	locale, err = resolveCreateSourceLocale(t.Context(), store, "", "")
	if err != nil || locale != translation.DefaultLocale {
		t.Fatalf("default locale fallback=(%q, %v)", locale, err)
	}
}

func TestNormalizeWorkDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizeWorkDocumentLocale("zh-TW"); err != nil || locale != "zh-TW" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizeWorkDocumentLocale(locale); errs.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizeWorkDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
