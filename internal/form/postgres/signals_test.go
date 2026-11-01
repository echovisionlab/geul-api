package formpostgres

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/form"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestContentUpdatedEventRendersAnAIDocumentEdit(t *testing.T) {
	if event := contentUpdatedEvent(form.ContentUpdate{FormID: "form"}); event != nil {
		t.Fatalf("an update without fields rendered %+v", event)
	}
	targetRevision := "target-revision"
	event := contentUpdatedEvent(form.ContentUpdate{
		FormID: "form", Locale: "ko", Fields: []string{"title", "document.content"}, DocumentRevision: "revision",
		Contributor: "member", TargetRevision: &targetRevision,
	})
	if event.GetEntityType() != managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM || event.GetEntityId() != "form" ||
		event.GetDocumentRevision() != "revision" || event.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI ||
		event.GetLocale() != "ko" || !event.GetLocaleExists() || event.GetTargetRevision() != targetRevision ||
		event.GetDocumentStateChanged() || len(event.GetContributorMemberIds()) != 1 || event.GetContributorMemberIds()[0] != "member" ||
		len(event.GetChangedFields()) != 2 || event.GetChangedFields()[1].GetPath() != "document.content" ||
		event.GetChangedFields()[1].GetKind() != managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT || event.GetTimestampMs() == 0 {
		t.Fatalf("event = %+v", event)
	}
	deleted := contentUpdatedEvent(form.ContentUpdate{FormID: "form", Locale: "ko", Fields: []string{"document.content"}, LocaleDeleted: true})
	if deleted.GetLocaleExists() || deleted.TargetRevision != nil {
		t.Fatalf("deleted event = %+v", deleted)
	}
	source := contentUpdatedEvent(form.ContentUpdate{FormID: "form", Locale: "en", Fields: []string{"title"}, SourceLocale: true})
	if !source.GetDocumentStateChanged() {
		t.Fatalf("source event = %+v", source)
	}
}
