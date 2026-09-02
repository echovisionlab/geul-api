package form

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestValidateFormAIDocumentTargetSchemaPreservesExplicitEmpty(t *testing.T) {
	source := []byte(`{"id":"schema","steps":[{"id":"step-a","title":"Contact","fields":[{"id":"field-a","key":"email","type":"email","label":"Email","validation":{"validators":[{"id":"required","predicate":"required","message":"Required"}]}}]}]}`)
	target := []byte(`{"id":"schema","steps":[{"id":"step-a","title":"","fields":[{"id":"field-a","key":"email","type":"email","label":"","validation":{"validators":[{"id":"required","predicate":"required","message":""}]}}]}]}`)
	if err := validateFormAIDocumentTargetSchema(source, target); err != nil {
		t.Fatal(err)
	}
}

func TestValidateFormAIDocumentTargetSchemaRejectsSourceOwnedChange(t *testing.T) {
	source := []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"email","type":"email"}]}]}`)
	target := []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"changed","type":"text"}]}]}`)
	if err := validateFormAIDocumentTargetSchema(source, target); err == nil {
		t.Fatal("source-owned target change was accepted")
	}
}

func TestValidateFormAIDocumentTargetSchemaRejectsLocaleNamedKeyOutsideOwnedPath(t *testing.T) {
	source := []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"email","type":"email","condition":{"message":"source-owned"}}]}]}`)
	target := []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"email","type":"email","condition":{"message":"changed"}}]}]}`)
	if err := validateFormAIDocumentTargetSchema(source, target); err == nil {
		t.Fatal("source-owned nested message change was accepted")
	}
}

func TestFormAIDocumentContentUpdatedEventCoversTranslationLifecycle(t *testing.T) {
	if event := formAIDocumentContentUpdatedEvent(AIDocumentMutation{}, AIDocumentMutationResult{}); event != nil {
		t.Fatalf("no-op event = %+v", event)
	}
	targetRevision := "target-revision"
	event := formAIDocumentContentUpdatedEvent(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", SetSchema: true, ExpectedSource: "en", Locale: "ko"},
		AIDocumentMutationResult{DocumentRevision: "revision", TargetRevision: &targetRevision, Changed: true},
	)
	if event.GetEntityId() != "form" || event.GetDocumentRevision() != "revision" ||
		event.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI ||
		event.GetLocale() != "ko" || !event.GetLocaleExists() || event.GetTargetRevision() != targetRevision ||
		event.GetDocumentStateChanged() || len(event.GetContributorMemberIds()) != 1 || event.GetContributorMemberIds()[0] != "member" {
		t.Fatalf("event = %+v", event)
	}
	sourceEvent := formAIDocumentContentUpdatedEvent(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", SetTitle: true, ExpectedSource: "en", Locale: "en"},
		AIDocumentMutationResult{DocumentRevision: "next", Changed: true},
	)
	if sourceEvent == nil || !sourceEvent.GetDocumentStateChanged() || sourceEvent.GetLocale() != "en" || !sourceEvent.GetLocaleExists() || sourceEvent.TargetRevision != nil {
		t.Fatalf("source event = %+v", sourceEvent)
	}
	deleteEvent := formAIDocumentContentUpdatedEvent(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", DeleteTranslation: true, ExpectedSource: "en", Locale: "ko"},
		AIDocumentMutationResult{DocumentRevision: "next", Changed: true},
	)
	if deleteEvent == nil || deleteEvent.GetLocale() != "ko" || deleteEvent.GetLocaleExists() || deleteEvent.TargetRevision != nil || deleteEvent.GetDocumentStateChanged() {
		t.Fatalf("delete event = %+v", deleteEvent)
	}
}

func TestFormAIDocumentMutationRejectsMissingLocaleValue(t *testing.T) {
	if err := validateFormAIDocumentMutation(AIDocumentMutation{
		FormID: "019c89aa-6798-7a37-8532-11e03f729c35", Locale: "ko",
		ExpectedSource: "en", ExpectedDocumentRevision: "revision", ExpectedPresence: true,
		SetTitle: true,
	}); err == nil {
		t.Fatal("missing target title was accepted")
	}
}

func TestFormAIDocumentJSONEqualityIsSemantic(t *testing.T) {
	if !formAIDocumentJSONEqual([]byte(`{"id":"schema","steps":[]}`), []byte("{\n  \"steps\": [],\n  \"id\": \"schema\"\n}")) {
		t.Fatal("equivalent Form schemas were treated as changed")
	}
}
