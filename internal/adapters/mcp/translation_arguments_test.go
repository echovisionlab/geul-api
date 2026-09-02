package mcp

import (
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestTranslationLocalesPreserveExplicitOrderAndRejectImplicitOrDuplicateTargets(t *testing.T) {
	locales, err := translationLocales([]core.Locale{"en", "ja"})
	if err != nil {
		t.Fatalf("translationLocales() error = %v", err)
	}
	if !reflect.DeepEqual(locales, []string{"en", "ja"}) {
		t.Fatalf("translationLocales() = %v", locales)
	}
	for _, input := range [][]core.Locale{nil, {"en", "en"}} {
		if _, err := translationLocales(input); err == nil {
			t.Fatalf("translationLocales(%v) succeeded", input)
		}
	}
}

func TestTranslationJobStatusInputUsesResponseVocabulary(t *testing.T) {
	for name, status := range translationJobStatusesByName {
		if got := translationJobStatuses[status]; got != name {
			t.Fatalf("status round trip %q -> %v -> %q", name, status, got)
		}
	}
	for _, removed := range []string{"applied", "failed", "cancelled"} {
		if _, ok := translationJobStatusesByName[removed]; ok {
			t.Fatalf("removed terminal Translation Job status %q is still accepted", removed)
		}
		if _, err := translationJobsListRequest(translationJobsListArguments{Statuses: []string{removed}}); err == nil {
			t.Fatalf("translationJobsListRequest accepted removed terminal status %q", removed)
		}
	}
}

func TestTranslationJobSortDoesNotExposeRemovedSourceRevision(t *testing.T) {
	for _, field := range []string{"requested_at", "updated_at", "target_locale", "status"} {
		if !validTranslationJobSort(field) {
			t.Fatalf("validTranslationJobSort(%q) = false", field)
		}
	}
	if validTranslationJobSort("source_revision") {
		t.Fatal("removed source_revision is still accepted as a Translation Job sort")
	}
}
