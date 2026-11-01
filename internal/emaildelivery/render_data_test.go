package emaildelivery

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/sitelogo"
)

func TestBuildEmailRenderDataUsesCanonicalSiteTitleAndRuntimeOrigin(t *testing.T) {
	data := buildEmailRenderDataWithDefaults(
		"https://runtime.example.com",
		"ko",
		map[string]string{"site_origin": "https://input.example.com"},
		&RenderDefaults{
			SiteTitle: " Example Studio ",
		},
	)

	if got := data["site_name"]; got != "Example Studio" {
		t.Fatalf("expected canonical site_name, got %q", got)
	}
	if got := data["site_origin"]; got != "https://runtime.example.com" {
		t.Fatalf("expected runtime site_origin to override input, got %q", got)
	}
	if _, ok := data["site_url"]; ok {
		t.Fatal("retired site location alias must not be projected")
	}
}

func TestBuildEmailRenderDataResolvesLightAndDarkEmailLogos(t *testing.T) {
	light := "https://cdn.example.com/asset/light.png"
	dark := "https://cdn.example.com/asset/dark.png"
	for _, tc := range []struct {
		name                string
		logos               sitelogo.Email[string]
		input               map[string]string
		wantLight, wantDark string
	}{
		{name: "both themes", logos: sitelogo.Email[string]{Light: &light, Dark: &dark}, wantLight: light, wantDark: dark},
		{name: "light only serves both", logos: sitelogo.Email[string]{Light: &light}, wantLight: light, wantDark: light},
		{name: "dark only serves both", logos: sitelogo.Email[string]{Dark: &dark}, wantLight: dark, wantDark: dark},
		{name: "none renders empty placeholders", logos: sitelogo.Email[string]{}},
		{
			name: "explicit template input wins", logos: sitelogo.Email[string]{Light: &light, Dark: &dark},
			input:     map[string]string{"logo_email_url": "https://override/l.png", "logo_email_dark_url": "https://override/d.png"},
			wantLight: "https://override/l.png", wantDark: "https://override/d.png",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			if input == nil {
				input = map[string]string{}
			}
			data := buildEmailRenderDataWithDefaults("https://studio.example.com", "ko", input, &RenderDefaults{Logos: tc.logos})
			if got := data["logo_email_url"]; got != tc.wantLight {
				t.Errorf("logo_email_url = %q, want %q", got, tc.wantLight)
			}
			if got := data["logo_email_dark_url"]; got != tc.wantDark {
				t.Errorf("logo_email_dark_url = %q, want %q", got, tc.wantDark)
			}
		})
	}
}

func TestBuildEmailRenderDataNormalizesRecipientAliasesFromEmail(t *testing.T) {
	data := buildEmailRenderDataWithDefaults(
		"https://studio.example.com",
		"en",
		map[string]string{
			"recipient_email": "johndoe@example.com",
		},
		nil,
	)

	if got := data["name"]; got != "johndoe" {
		t.Fatalf("expected name fallback from email, got %q", got)
	}
	if got := data["recipient_name"]; got != "johndoe" {
		t.Fatalf("expected recipient_name fallback from email, got %q", got)
	}
	if _, ok := data["subscriber_name"]; ok {
		t.Fatal("subscriber_name legacy alias must not be projected")
	}
	if _, ok := data["subscriber_email"]; ok {
		t.Fatal("subscriber_email legacy alias must not be projected")
	}
	if got := data["identity_email"]; got != "johndoe@example.com" {
		t.Fatalf("expected identity_email alias, got %q", got)
	}
	if got := data["email_lang"]; got != "en" {
		t.Fatalf("expected email_lang=en, got %q", got)
	}
	if got := data["email_direction"]; got != "ltr" {
		t.Fatalf("expected email_direction=ltr, got %q", got)
	}
	if got := data["email_font_family"]; got != "'Geul Sans', 'Geul Emoji', sans-serif" {
		t.Fatalf("unexpected email_font_family: %q", got)
	}
	if got := data["email_font_stylesheet_url"]; got != "https://static.rawsector.com/fonts/geul-base.css" {
		t.Fatalf("unexpected email_font_stylesheet_url: %q", got)
	}
}

func TestBuildEmailRenderDataUsesLocaleSpecificEmailFontMetadata(t *testing.T) {
	data := buildEmailRenderDataWithDefaults(
		"https://studio.example.com",
		"ko",
		map[string]string{},
		nil,
	)

	if got := data["email_lang"]; got != "ko" {
		t.Fatalf("expected email_lang=ko, got %q", got)
	}
	if got := data["email_direction"]; got != "ltr" {
		t.Fatalf("expected email_direction=ltr, got %q", got)
	}
	if got := data["email_font_family"]; got != "'Geul Sans', 'Geul Sans Han TC', 'Geul Emoji', sans-serif" {
		t.Fatalf("unexpected email_font_family: %q", got)
	}
	if got := data["email_font_stylesheet_url"]; got != "https://static.rawsector.com/fonts/geul-ko.css" {
		t.Fatalf("unexpected email_font_stylesheet_url: %q", got)
	}
}

func TestNormalizeEmailRenderLocaleUsesCanonicalRegistry(t *testing.T) {
	tests := map[string]string{
		"en-US":   "en",
		"pt_br":   "pt-BR",
		"zh-Hans": "zh-CN",
		"zh-HK":   "zh-TW",
		"unknown": "en",
	}
	for input, want := range tests {
		if got := normalizeEmailRenderLocale(input); got != want {
			t.Fatalf("normalizeEmailRenderLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReservedSiteVariablesOverrideCallerCaseAliases(t *testing.T) {
	input := map[string]string{"site.title": "attacker", "SITE.TITLE": "attacker", "contact.legalemail": "attacker", "CONTACT.SUPPORTEMAIL": "attacker", "contact.supportEmail": "attacker", "contact.privacyEmail": "attacker", "CONTACT.EMAIL": "attacker", "contact.email": "attacker", "site.url": "https://evil.test"}
	data := buildEmailRenderDataWithDefaults("https://www.rawsector.com/", "en", input, &RenderDefaults{SiteTitle: "rawsector", CompanyName: "Company", LegalEmail: "legal@example.test", ContactEmail: "contact@example.test"})
	for key, want := range map[string]string{"site.title": "rawsector", "site.url": "https://www.rawsector.com", "site.companyName": "Company", "contact.legalEmail": "legal@example.test", "contact.email": "contact@example.test"} {
		if data[key] != want {
			t.Fatalf("%s = %q, want %q", key, data[key], want)
		}
	}
	for _, key := range []string{"SITE.TITLE", "CONTACT.SUPPORTEMAIL", "contact.supportEmail", "contact.privacyEmail"} {
		if _, ok := data[key]; ok {
			t.Fatalf("caller reserved key %s survived", key)
		}
	}
	if input["site.title"] != "attacker" {
		t.Fatal("input mutated")
	}
}
