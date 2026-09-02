package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestSourceLocaleUpdatedEventUsesCompleteTranslationCatalog(t *testing.T) {
	for _, definition := range translation.Definitions() {
		event := buildTypedSourceLocaleUpdatedEvent(string(definition.Kind), "entity-1", "revision-1")
		if event == nil {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q) returned nil", definition.Kind)
		}
		if event.EntityType != definition.ContentEntityType {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q).entity_type = %v, want %v", definition.Kind, event.EntityType, definition.ContentEntityType)
		}
		if !event.DocumentStateChanged || event.DocumentRevision == nil || *event.DocumentRevision != "revision-1" {
			t.Fatalf("buildTypedSourceLocaleUpdatedEvent(%q) lost document revision fence: %#v", definition.Kind, event)
		}
	}
}

func TestSourceLocaleUpdatedEventRejectsUnknownDomain(t *testing.T) {
	if event := buildTypedSourceLocaleUpdatedEvent("unknown", "entity-1", "revision-1"); event != nil {
		t.Fatalf("unknown Translation domain produced event %#v", event)
	}
}
