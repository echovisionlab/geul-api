package emaildelivery

import (
	"context"
	"testing"
)

func TestBuildEmailRenderDataUsesCanonicalSiteTitleAndRuntimeOrigin(t *testing.T) {
	data := buildEmailRenderDataWithDefaults(
		"https://cdn.example.com",
		"https://runtime.example.com",
		"ko",
		map[string]string{"site_origin": "https://input.example.com"},
		&emailSiteSettings{
			SiteTitle: " Example Studio ",
		},
		nil,
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

func TestBuildEmailRenderDataUsesOriginalEmailLogoURL(t *testing.T) {
	logoFileID := "f77f6bcf-61f4-4c60-9f59-9c3efdf4a0a1"
	logoURL := "https://cdn.example.com/media/site/logo/" + logoFileID + ".png"

	data := buildEmailRenderDataWithDefaults(
		"https://cdn.example.com",
		"https://studio.example.com",
		"ko",
		map[string]string{},
		&emailSiteSettings{LogoEmailFileID: &logoFileID},
		&logoURL,
	)

	if got := data["logo_email_url"]; got != logoURL {
		t.Fatalf("expected original media logo URL %q, got %q", logoURL, got)
	}
}

func TestBuildEmailRenderDataNormalizesRecipientAliasesFromEmail(t *testing.T) {
	data := BuildEmailRenderData(
		context.Background(),
		nil,
		"https://cdn.example.com",
		"https://studio.example.com",
		"en",
		map[string]string{
			"recipient_email": "johndoe@example.com",
		},
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
	if got := data["email_font_family"]; got != "'Noto Sans', 'Noto Color Emoji', sans-serif" {
		t.Fatalf("unexpected email_font_family: %q", got)
	}
	if got := data["email_font_stylesheet_url"]; got != "https://cdn.example.com/fonts/css2?family=Noto+Sans:wght@100..900&family=Noto+Sans+Arabic:wght@100..900&family=Noto+Sans+KR:wght@100..900&family=Noto+Sans+JP:wght@100..900&family=Noto+Sans+SC:wght@100..900&family=Noto+Sans+TC:wght@100..900&family=Noto+Sans+HK:wght@100..900&family=Noto+Sans+Mono:wght@100..900&family=Noto+Color+Emoji&display=swap" {
		t.Fatalf("unexpected email_font_stylesheet_url: %q", got)
	}
}

func TestBuildEmailRenderDataUsesLocaleSpecificEmailFontMetadata(t *testing.T) {
	data := BuildEmailRenderData(
		context.Background(),
		nil,
		"https://cdn.example.com/",
		"https://studio.example.com",
		"ko",
		map[string]string{},
	)

	if got := data["email_lang"]; got != "ko" {
		t.Fatalf("expected email_lang=ko, got %q", got)
	}
	if got := data["email_direction"]; got != "ltr" {
		t.Fatalf("expected email_direction=ltr, got %q", got)
	}
	if got := data["email_font_family"]; got != "'Noto Sans KR', 'Noto Sans', 'Noto Color Emoji', sans-serif" {
		t.Fatalf("unexpected email_font_family: %q", got)
	}
	if got := data["email_font_stylesheet_url"]; got != "https://cdn.example.com/fonts/css2?family=Noto+Sans:wght@100..900&family=Noto+Sans+Arabic:wght@100..900&family=Noto+Sans+KR:wght@100..900&family=Noto+Sans+JP:wght@100..900&family=Noto+Sans+SC:wght@100..900&family=Noto+Sans+TC:wght@100..900&family=Noto+Sans+HK:wght@100..900&family=Noto+Sans+Mono:wght@100..900&family=Noto+Color+Emoji&display=swap" {
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
