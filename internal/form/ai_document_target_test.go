package form

import (
	"reflect"
	"testing"
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

func TestFormAIDocumentContentUpdateCoversTranslationLifecycle(t *testing.T) {
	if update := formAIDocumentContentUpdate(AIDocumentMutation{}, AIDocumentMutationResult{}); len(update.Fields) != 0 {
		t.Fatalf("no-op update = %+v", update)
	}
	targetRevision := "target-revision"
	update := formAIDocumentContentUpdate(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", SetSchema: true, ExpectedSource: "en", Locale: "ko"},
		AIDocumentMutationResult{DocumentRevision: "revision", TargetRevision: &targetRevision, Changed: true},
	)
	if !reflect.DeepEqual(update, ContentUpdate{
		FormID: "form", Locale: "ko", Fields: []string{"document.content"}, DocumentRevision: "revision",
		Contributor: "member", TargetRevision: &targetRevision,
	}) {
		t.Fatalf("update = %+v", update)
	}
	sourceUpdate := formAIDocumentContentUpdate(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", SetTitle: true, ExpectedSource: "en", Locale: "en"},
		AIDocumentMutationResult{DocumentRevision: "next", Changed: true},
	)
	if !sourceUpdate.SourceLocale || sourceUpdate.Locale != "en" || sourceUpdate.LocaleDeleted || sourceUpdate.TargetRevision != nil ||
		!reflect.DeepEqual(sourceUpdate.Fields, []string{"title"}) {
		t.Fatalf("source update = %+v", sourceUpdate)
	}
	deleteUpdate := formAIDocumentContentUpdate(
		AIDocumentMutation{FormID: "form", ContributorMemberID: "member", DeleteTranslation: true, ExpectedSource: "en", Locale: "ko"},
		AIDocumentMutationResult{DocumentRevision: "next", TargetRevision: &targetRevision, Changed: true},
	)
	if deleteUpdate.Locale != "ko" || !deleteUpdate.LocaleDeleted || deleteUpdate.TargetRevision != nil || deleteUpdate.SourceLocale {
		t.Fatalf("delete update = %+v", deleteUpdate)
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
