package public

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestArtistSummaryCarriesSocialLinks(t *testing.T) {
	now := time.Date(2026, time.April, 8, 12, 0, 0, 0, time.UTC)
	slug := "artist-slug"
	countryCode := "KR"
	socialLinks := map[string]string{
		"instagram": "https://instagram.com/example",
		"x":         "https://x.com/example",
	}

	artist := model.Artist{
		ID:          "artist-1",
		Name:        "Artist",
		Slug:        &slug,
		CountryCode: &countryCode,
		SocialLinks: socialLinks,
		PublishedAt: &now,
	}

	summary := buildArtistSummary(&artist)

	if summary.SocialLinks["instagram"] != "https://instagram.com/example" {
		t.Fatalf("expected instagram social link, got %v", summary.SocialLinks)
	}
	if summary.SocialLinks["x"] != "https://x.com/example" {
		t.Fatalf("expected x social link, got %v", summary.SocialLinks)
	}
}
