package localization

import (
	"testing"
)

func TestValidateRuntimeCatalog(t *testing.T) {
	t.Parallel()
	locales := canonicalRuntimeLocaleFixtures()
	if err := validateRuntimeCatalog(locales); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
}

func TestValidateRuntimeCatalogRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		locales []RuntimeLocale
	}{
		{
			name: "missing canonical code",
			locales: []RuntimeLocale{
				{Code: LocaleEnglish, DisplayName: "English", Dir: "ltr"},
			},
		},
		{
			name: "noncanonical code",
			locales: []RuntimeLocale{
				{Code: "ko-KR", DisplayName: "Korean", Dir: "ltr"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateRuntimeCatalog(test.locales); err == nil {
				t.Fatal("invalid runtime catalog accepted")
			}
		})
	}
}

func canonicalRuntimeLocaleFixtures() []RuntimeLocale {
	locales := make([]RuntimeLocale, 0, len(canonicalLocaleCodes))
	for _, code := range canonicalLocaleCodes {
		locales = append(locales, RuntimeLocale{
			Code:        code,
			DisplayName: code,
			Dir:         "ltr",
		})
	}
	return locales
}
