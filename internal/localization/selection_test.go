package localization

import (
	"reflect"
	"testing"
)

func defaultServingPolicy() ServingPolicy {
	return ServingPolicy{DefaultLocale: LocaleEnglish}
}

func TestSelectLocalizedProjection(t *testing.T) {
	t.Parallel()
	policy := defaultServingPolicy()

	tests := []struct {
		name       string
		requested  string
		source     string
		candidates map[string]struct{}
		want       Decision
	}{
		{
			name:      "source locale",
			requested: "ko-KR",
			source:    LocaleKorean,
			candidates: map[string]struct{}{
				LocaleKorean: {},
			},
			want: Decision{
				RequestedLocale: LocaleKorean,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleKorean,
				IsOriginal:      true,
				HasCandidate:    true,
			},
		},
		{
			name:      "exact stored target",
			requested: LocaleKorean,
			source:    LocaleEnglish,
			candidates: map[string]struct{}{
				LocaleKorean: {},
			},
			want: Decision{
				RequestedLocale: LocaleKorean,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleEnglish,
				HasCandidate:    true,
			},
		},
		{
			name:      "stored target is selected without target metadata",
			requested: LocaleKorean,
			source:    LocaleFrench,
			candidates: map[string]struct{}{
				LocaleKorean: {},
			},
			want: Decision{
				RequestedLocale: LocaleKorean,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleFrench,
				HasCandidate:    true,
			},
		},
		{
			name:      "missing requested locale falls back to stored default locale",
			requested: LocaleDutch,
			source:    LocaleKorean,
			candidates: map[string]struct{}{
				LocaleKorean:   {},
				LocaleEnglish:  {},
				LocaleJapanese: {},
			},
			want: Decision{
				RequestedLocale: LocaleDutch,
				DisplayedLocale: LocaleEnglish,
				SourceLocale:    LocaleKorean,
				IsFallback:      true,
				FallbackReason:  FallbackSource,
				HasCandidate:    true,
			},
		},
		{
			name:      "missing requested and default locales fall back to source",
			requested: LocaleDutch,
			source:    LocaleKorean,
			candidates: map[string]struct{}{
				LocaleKorean:   {},
				LocaleJapanese: {},
			},
			want: Decision{
				RequestedLocale: LocaleDutch,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleKorean,
				IsFallback:      true,
				IsOriginal:      true,
				FallbackReason:  FallbackSource,
				HasCandidate:    true,
			},
		},
		{
			name:      "missing requested default locale falls back to source",
			requested: LocaleEnglish,
			source:    LocaleKorean,
			candidates: map[string]struct{}{
				LocaleKorean: {},
			},
			want: Decision{
				RequestedLocale: LocaleEnglish,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleKorean,
				IsFallback:      true,
				IsOriginal:      true,
				FallbackReason:  FallbackSource,
				HasCandidate:    true,
			},
		},
		{
			name:      "default locale equal to source is the source fallback",
			requested: LocaleDutch,
			source:    LocaleEnglish,
			candidates: map[string]struct{}{
				LocaleEnglish: {},
				LocaleKorean:  {},
			},
			want: Decision{
				RequestedLocale: LocaleDutch,
				DisplayedLocale: LocaleEnglish,
				SourceLocale:    LocaleEnglish,
				IsFallback:      true,
				IsOriginal:      true,
				FallbackReason:  FallbackSource,
				HasCandidate:    true,
			},
		},
		{
			name:       "source fallback without row",
			requested:  LocaleJapanese,
			source:     LocaleKorean,
			candidates: map[string]struct{}{},
			want: Decision{
				RequestedLocale: LocaleJapanese,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleKorean,
				IsFallback:      true,
				IsOriginal:      true,
				FallbackReason:  FallbackSource,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Select(test.requested, test.source, test.candidates, policy)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Select() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSelectUsesConfiguredDefaultLocale(t *testing.T) {
	t.Parallel()
	storedLocales := map[string]struct{}{
		LocaleKorean:  {},
		LocaleEnglish: {},
		LocaleGerman:  {},
	}

	got := Select(LocaleArabic, LocaleKorean, storedLocales, ServingPolicy{DefaultLocale: LocaleGerman})
	if got.DisplayedLocale != LocaleGerman || !got.IsFallback || got.IsOriginal || !got.HasCandidate {
		t.Fatalf("Select() = %#v, want configured default locale fallback", got)
	}
}

func TestFallbackLocales(t *testing.T) {
	t.Parallel()
	policy := defaultServingPolicy()
	tests := []struct {
		name      string
		requested string
		source    string
		want      []string
	}{
		{name: "distinct", requested: LocaleDutch, source: LocaleKorean, want: []string{LocaleDutch, LocaleEnglish, LocaleKorean}},
		{name: "requested is default", requested: LocaleEnglish, source: LocaleKorean, want: []string{LocaleEnglish, LocaleKorean}},
		{name: "source is default", requested: LocaleDutch, source: LocaleEnglish, want: []string{LocaleDutch, LocaleEnglish}},
		{name: "requested is source", requested: "ko-KR", source: LocaleKorean, want: []string{LocaleKorean}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := FallbackLocales(test.requested, test.source, policy)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("FallbackLocales() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSelectSourceFallbackUsesStoredSourceRow(t *testing.T) {
	t.Parallel()
	policy := defaultServingPolicy()
	storedLocales := map[string]struct{}{
		LocaleFrench: {},
	}

	got := Select(LocaleKorean, LocaleFrench, storedLocales, policy)
	if got.DisplayedLocale != LocaleFrench || got.FallbackReason != FallbackSource || !got.HasCandidate {
		t.Fatalf("Select() = %#v, want stored source fallback", got)
	}
}

func TestAvailableLocales(t *testing.T) {
	t.Parallel()
	policy := defaultServingPolicy()
	got := AvailableLocales(LocaleEnglish, []string{
		"ko-KR",
		LocaleJapanese,
		LocaleFrench,
		"invalid",
		LocaleKorean,
	}, policy)
	want := []string{LocaleEnglish, LocaleFrench, LocaleJapanese, LocaleKorean}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AvailableLocales() = %v, want %v", got, want)
	}
}
