package post

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

func TestValidateCompiledPostAIDocumentMutationKeepsLockedStateAuthoritative(t *testing.T) {
	postID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "target-revision"
	state := AIDocumentState{
		PostID: postID.String(), ContentDocumentID: documentID.String(),
		SourceLocale: "en", RequestedLocale: "ko", LocaleExists: true,
		DocumentRevision: revision.String(), TargetRevision: &targetRevision,
		ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentMutation{
		PostID: state.PostID, Locale: state.RequestedLocale,
		ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
		ExpectedRevision: revision, ExpectedTargetRevision: &targetRevision,
		ContributorMemberID: contributor, Batch: contentblock.Batch{DocumentID: documentID},
	}
	resolved, err := validateCompiledPostAIDocumentMutation(state, valid)
	if err != nil || resolved != documentID {
		t.Fatalf("valid compiled mutation = (%s, %v), want %s", resolved, err, documentID)
	}

	tests := []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "Post", mutate: func(m *AIDocumentMutation) { m.PostID = uuid.NewString() }},
		{name: "locale", mutate: func(m *AIDocumentMutation) { m.Locale = "fr" }},
		{name: "contributor", mutate: func(m *AIDocumentMutation) { m.ContributorMemberID = uuid.New() }},
		{name: "source locale", mutate: func(m *AIDocumentMutation) { m.ObservedSourceLocale = "fr" }},
		{name: "locale presence", mutate: func(m *AIDocumentMutation) { m.ObservedLocaleExists = false }},
		{name: "content document", mutate: func(m *AIDocumentMutation) { m.Batch.DocumentID = uuid.New() }},
		{name: "document revision", mutate: func(m *AIDocumentMutation) { m.ExpectedRevision = uuid.New() }},
		{name: "target revision", mutate: func(m *AIDocumentMutation) {
			changed := "other-target-revision"
			m.ExpectedTargetRevision = &changed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := valid
			test.mutate(&mutation)
			_, err := validateCompiledPostAIDocumentMutation(state, mutation)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("mismatched compiled mutation error = %v", err)
			}
		})
	}
}

func TestPostAIDocumentUpdatedEventUsesExactStoreChangeKinds(t *testing.T) {
	mutation := AIDocumentMutation{
		PostID: uuid.NewString(), Locale: "en", ObservedSourceLocale: "en",
		ContributorMemberID: uuid.New(),
	}
	revision := uuid.New()
	if event := postAIDocumentUpdatedEvent(mutation, contentblock.Result{DocumentRevision: revision}, &postAIDocumentMutationEffects{}); event != nil {
		t.Fatalf("no-op event = %+v", event)
	}
	if event := postAIDocumentUpdatedEvent(mutation, contentblock.Result{
		DocumentRevision: revision, Changed: true, MetadataChanged: true,
	}, &postAIDocumentMutationEffects{changedFields: []string{"title"}}); event != nil {
		t.Fatalf("target-only event = %+v", event)
	}

	event := postAIDocumentUpdatedEvent(mutation, contentblock.Result{
		DocumentRevision: revision, Changed: true, ContentChanged: true,
		MetadataChanged: true, TranslationSourceChanged: true,
	}, &postAIDocumentMutationEffects{changedFields: []string{"title"}})
	if event == nil {
		t.Fatal("source event is missing")
	}
	if event.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI ||
		event.GetDocumentRevision() != revision.String() || len(event.GetContributorMemberIds()) != 1 ||
		event.GetContributorMemberIds()[0] != mutation.ContributorMemberID.String() {
		t.Fatalf("source event envelope = %+v", event)
	}
	if len(event.GetChangedFields()) != 2 || event.GetChangedFields()[0].GetPath() != "title" ||
		event.GetChangedFields()[1].GetPath() != "document.content" {
		t.Fatalf("source event fields = %+v", event.GetChangedFields())
	}
}

func TestPostAIDocumentMutationEffectsDeduplicatesFields(t *testing.T) {
	effects := &postAIDocumentMutationEffects{}
	effects.addField("title")
	effects.addField("title")
	if len(effects.changedFields) != 1 || effects.changedFields[0] != "title" {
		t.Fatalf("effects = %+v", effects)
	}
}
