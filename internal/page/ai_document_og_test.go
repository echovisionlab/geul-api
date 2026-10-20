package page

import "testing"

func TestAIDocumentOGRequestFollowsTitleChanges(t *testing.T) {
	text := func(value string) *string { return &value }
	for _, tc := range []struct {
		name     string
		state    AIDocumentState
		mutation AIDocumentMutation
		want     bool
	}{
		{"first title of a new translation", AIDocumentState{LocaleExists: false}, AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetTitle: true, Title: text("Support")}}, true},
		{"different title", AIDocumentState{LocaleExists: true, Title: text("Old")}, AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetTitle: true, Title: text("New")}}, true},
		{"same title", AIDocumentState{LocaleExists: true, Title: text("Same")}, AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetTitle: true, Title: text("Same")}}, false},
		{"title untouched", AIDocumentState{LocaleExists: true, Title: text("Same")}, AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetSummary: true}}, false},
		{"translation deleted", AIDocumentState{LocaleExists: true, Title: text("Old")}, AIDocumentMutation{DeleteTranslation: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pageAIDocumentTitleChanged(tc.state, tc.mutation); got != tc.want {
				t.Fatalf("pageAIDocumentTitleChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}
