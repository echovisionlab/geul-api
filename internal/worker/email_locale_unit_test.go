package worker

import (
	"context"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestNormalizeEmailLocale(t *testing.T) {
	if got := normalizeEmailTargetLocale(nil); got != "" {
		t.Fatalf("normalizeEmailTargetLocale(nil) = %q, want empty", got)
	}

	locale := " pt "
	if got := normalizeEmailTargetLocale(&locale); got != "pt-BR" {
		t.Fatalf("normalizeEmailTargetLocale(pt) = %q, want pt-BR", got)
	}
}

func TestResolveEmailTargetLocaleUsesExplicitLocaleWithoutLookup(t *testing.T) {
	locale := " pt "
	handlers := &Handlers{}

	got := handlers.resolveEmailTargetLocale(context.Background(), &managev1.SendEmailEvent{
		Recipient: "reader@example.com",
		Locale:    &locale,
	})
	if got != "pt-BR" {
		t.Fatalf("resolveEmailTargetLocale() = %q, want pt-BR", got)
	}
}

func TestResolveEmailTargetLocaleHandlesNilJob(t *testing.T) {
	handlers := &Handlers{}

	if got := handlers.resolveEmailTargetLocale(context.Background(), nil); got != "" {
		t.Fatalf("resolveEmailTargetLocale(nil) = %q, want empty", got)
	}
}
