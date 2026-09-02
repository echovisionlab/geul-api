package localization

import (
	"testing"
)

func TestNormalizeSupportedLocale(t *testing.T) {
	tests := map[string]string{
		"en-US":   "en",
		"ko_KR":   "ko",
		"zh-Hant": "zh-TW",
		"zh-SG":   "zh-CN",
		"pt-PT":   "pt-PT",
		"es-419":  "es-419",
		"es-MX":   "es-419",
		"es-ES":   "es",
		"id-ID":   "id",
		"vi-VN":   "vi",
		"th-TH":   "th",
		"tr-TR":   "tr",
		"pl-PL":   "pl",
		"ru-RU":   "ru",
	}
	for input, want := range tests {
		got := NormalizeSupportedLocale(input)
		if got == nil || *got != want {
			t.Fatalf("NormalizeSupportedLocale(%q) = %v, want %q", input, got, want)
		}
	}
	if NormalizeSupportedLocale("xx-XX") != nil {
		t.Fatal("unsupported locale must return nil")
	}
}

func TestNormalizeSupportedLocaleCanonicalSet(t *testing.T) {
	want := []string{
		"en",
		"ko",
		"ja",
		"zh-CN",
		"zh-TW",
		"es",
		"es-419",
		"fr",
		"de",
		"pt-BR",
		"pt-PT",
		"it",
		"nl",
		"ar",
		"id",
		"vi",
		"th",
		"tr",
		"pl",
		"ru",
	}
	got := append([]string(nil), canonicalLocaleCodes[:]...)
	if len(got) != len(want) {
		t.Fatalf("canonical locale count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("canonical locale %d = %q, want %q", index, got[index], want[index])
		}
	}

	for _, locale := range want {
		got := NormalizeSupportedLocale(locale)
		if got == nil || *got != locale {
			t.Fatalf("NormalizeSupportedLocale(%q) = %v, want %q", locale, got, locale)
		}
	}

}

func TestNormalizeExactSupportedLocaleRejectsNonCanonicalIdentity(t *testing.T) {
	for _, locale := range CanonicalLocaleCodes() {
		got := NormalizeExactSupportedLocale(locale)
		if got == nil || *got != locale {
			t.Fatalf("NormalizeExactSupportedLocale(%q) = %v, want %q", locale, got, locale)
		}
	}

	for _, input := range []string{
		"", " ko", "ko ", "KO", "ko_KR", "ko-KR", "en-US", "zh_hant", "zh-Hant", "es-MX", "pt",
	} {
		if got := NormalizeExactSupportedLocale(input); got != nil {
			t.Fatalf("NormalizeExactSupportedLocale(%q) = %q, want nil", input, *got)
		}
	}
}

func TestNormalizeOptionalSupportedLocale(t *testing.T) {
	blank := "  "
	alias := "zh_Hant"
	invalid := "xx-YY"
	tests := []struct {
		name  string
		input *string
		want  string
		ok    bool
	}{
		{name: "nil", ok: true},
		{name: "blank", input: &blank, ok: true},
		{name: "alias", input: &alias, want: LocaleChineseTraditional, ok: true},
		{name: "invalid", input: &invalid, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := NormalizeOptionalSupportedLocale(test.input)
			if got != test.want || ok != test.ok {
				t.Fatalf("NormalizeOptionalSupportedLocale() = (%q, %t), want (%q, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}
