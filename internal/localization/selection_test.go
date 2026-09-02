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
			name:      "english target does not become a fallback",
			requested: LocaleJapanese,
			source:    LocaleKorean,
			candidates: map[string]struct{}{
				LocaleEnglish: {},
			},
			want: Decision{
				RequestedLocale: LocaleJapanese,
				DisplayedLocale: LocaleKorean,
				SourceLocale:    LocaleKorean,
				IsFallback:      true,
				IsOriginal:      true,
				FallbackReason:  FallbackSource,
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
