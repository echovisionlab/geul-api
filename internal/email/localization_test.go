package email

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestLocalesForEmailLookup(t *testing.T) {
	tests := []struct {
		name            string
		requestedLocale string
		want            []string
	}{
		{name: "empty locale", requestedLocale: "", want: nil},
		{name: "blank locale", requestedLocale: "  ", want: nil},
		{name: "default locale", requestedLocale: "en", want: []string{"en"}},
		{name: "requested locale only", requestedLocale: " ko ", want: []string{"ko"}},
		{name: "canonicalizes locale alias", requestedLocale: "pt_br", want: []string{"pt-BR"}},
		{name: "rejects unsupported locale", requestedLocale: "xx-YY", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := localesForEmailLookupWithPolicy(tt.requestedLocale, "", defaultEmailLocalizationPolicy())
			if len(got) != len(tt.want) {
				t.Fatalf("localesForEmailLookup() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("localesForEmailLookup() = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}

func TestSelectLocalizedEmailTemplateRow(t *testing.T) {
	sourceSubject := "Source"
	koreanSubject := "Korean"
	sourceHTML := "<p>Source</p>"
	koreanHTML := "<p>Korean</p>"
	rows := map[string]localizedEmailTemplateRow{
		"en": {
			Locale:      "en",
			Subject:     &sourceSubject,
			ContentHTML: &sourceHTML,
		},
		"ko": {
			Locale:      "ko",
			Subject:     &koreanSubject,
			ContentHTML: &koreanHTML,
		},
	}

	row, displayedLocale := selectLocalizedEmailTemplateRowWithPolicy(" ko ", "en", rows, defaultEmailLocalizationPolicy())
	if row == nil || row.Subject == nil || *row.Subject != koreanSubject || displayedLocale != "ko" {
		t.Fatalf("expected ko row/display locale, got row=%#v displayed=%q", row, displayedLocale)
	}

	row, displayedLocale = selectLocalizedEmailTemplateRowWithPolicy("ja", "en", rows, defaultEmailLocalizationPolicy())
	if row != nil || displayedLocale != "en" {
		t.Fatalf("expected canonical English source fallback, got row=%#v displayed=%q", row, displayedLocale)
	}

	row, displayedLocale = selectLocalizedEmailTemplateRowWithPolicy("", "en", rows, defaultEmailLocalizationPolicy())
	if row != nil || displayedLocale != "en" {
		t.Fatalf("expected source locale with no selected row, got row=%#v displayed=%q", row, displayedLocale)
	}
}

func TestApplyCanonicalEmailTemplateSourceRowOverlaysCanonicalSourceContent(t *testing.T) {
	sourceHTML := "<p>Source row body</p>"
	sourceSubject := "Source row subject"
	baseHTML := "<p>Base body</p>"
	source := model.EmailTemplate{
		ID:          "template-1",
		Subject:     "Base subject",
		ContentHTML: &baseHTML,
	}

	got := applyCanonicalEmailTemplateSourceRow(source, &canonicalEmailTemplateSourceRow{
		Subject:     &sourceSubject,
		ContentHTML: &sourceHTML,
	})

	if got.Subject != sourceSubject {
		t.Fatalf("expected canonical subject %q, got %q", sourceSubject, got.Subject)
	}
	if got.ContentHTML == nil || *got.ContentHTML != sourceHTML {
		t.Fatalf("expected canonical html %q, got %#v", sourceHTML, got.ContentHTML)
	}
}

func TestSelectLocalizedEmailLayoutRow(t *testing.T) {
	defaultHTML := "<main>{{content}}</main>"
	rows := map[string]localizedEmailLayoutRow{
		"en": {
			Locale:      "en",
			HTMLContent: &defaultHTML,
		},
	}

	row, displayedLocale := selectLocalizedEmailLayoutRowWithPolicy("en", "ko", rows, defaultEmailLocalizationPolicy())
	if row == nil || row.HTMLContent == nil || *row.HTMLContent != defaultHTML || displayedLocale != "en" {
		t.Fatalf("expected default layout row, got row=%#v displayed=%q", row, displayedLocale)
	}

	row, displayedLocale = selectLocalizedEmailLayoutRowWithPolicy("fr", "ko", nil, defaultEmailLocalizationPolicy())
	if row != nil || displayedLocale != "ko" {
		t.Fatalf("expected source layout fallback, got row=%#v displayed=%q", row, displayedLocale)
	}
}
