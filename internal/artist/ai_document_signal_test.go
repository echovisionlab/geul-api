package artist

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestArtistAIDocumentContentUpdatedEventCoversTranslationLifecycle(t *testing.T) {
	if event := artistAIDocumentContentUpdatedEvent(AIDocumentMutation{}, AIDocumentMutationResult{}, "member"); event != nil {
		t.Fatalf("no-op event = %+v", event)
	}
	event := artistAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ArtistID: "artist", Locale: "en", ExpectedSource: "en"},
		AIDocumentMutationResult{Revision: "revision", Changed: true},
		"member",
	)
	if event.GetEntityId() != "artist" || event.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI ||
		event.GetDocumentRevision() != "revision" || event.GetLocale() != "en" || !event.GetLocaleExists() ||
		event.GetTargetRevision() != "" || !event.GetDocumentStateChanged() || len(event.GetContributorMemberIds()) != 1 || event.GetContributorMemberIds()[0] != "member" {
		t.Fatalf("event = %+v", event)
	}
	targetRevision := "target-revision"
	target := artistAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ArtistID: "artist", Locale: "ko", ExpectedSource: "en"},
		AIDocumentMutationResult{Revision: "revision", TargetRevision: &targetRevision, Changed: true},
		"member",
	)
	if target.GetLocale() != "ko" || !target.GetLocaleExists() || target.GetTargetRevision() != targetRevision || target.GetDocumentStateChanged() {
		t.Fatalf("target event = %+v", target)
	}
	deleted := artistAIDocumentContentUpdatedEvent(
		AIDocumentMutation{ArtistID: "artist", Locale: "ko", ExpectedSource: "en", DeleteTranslation: true},
		AIDocumentMutationResult{Revision: "revision", Changed: true},
		"member",
	)
	if deleted.GetLocale() != "ko" || deleted.GetLocaleExists() || deleted.GetTargetRevision() != "" || deleted.GetDocumentStateChanged() {
		t.Fatalf("deleted event = %+v", deleted)
	}
}
