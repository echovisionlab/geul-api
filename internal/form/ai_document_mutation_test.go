package form

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestValidateCompiledFormAIDocumentMutationKeepsLockedStateAuthoritative(t *testing.T) {
	state := AIDocumentState{
		FormID: "019c89aa-6798-7a37-8532-11e03f729c35", SourceLocale: "en",
		Locale: "ko", LocaleExists: true, DocumentRevision: "41",
		TargetRevision: formTestStringPointer("51"),
		ViewerMemberID: "019c89aa-6798-7a37-8532-11e03f729c36",
	}
	valid := AIDocumentMutation{
		FormID: state.FormID, Locale: state.Locale, ExpectedSource: state.SourceLocale,
		ExpectedPresence: state.LocaleExists, ExpectedDocumentRevision: state.DocumentRevision,
		ExpectedTargetRevision: state.TargetRevision,
		ContributorMemberID:    state.ViewerMemberID, Noop: true,
	}
	if err := validateCompiledFormAIDocumentMutation(state, valid); err != nil {
		t.Fatalf("valid mutation error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "Form", mutate: func(m *AIDocumentMutation) { m.FormID = "019c89aa-6798-7a37-8532-11e03f729c37" }},
		{name: "locale", mutate: func(m *AIDocumentMutation) { m.Locale = "fr" }},
		{name: "contributor", mutate: func(m *AIDocumentMutation) { m.ContributorMemberID = "019c89aa-6798-7a37-8532-11e03f729c38" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutation := valid
			test.mutate(&mutation)
			if code := connect.CodeOf(validateCompiledFormAIDocumentMutation(state, mutation)); code != connect.CodeInvalidArgument {
				t.Fatalf("mismatch code = %s, want invalid_argument", code)
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "source", mutate: func(m *AIDocumentMutation) { m.ExpectedSource = "fr" }},
		{name: "presence", mutate: func(m *AIDocumentMutation) { m.ExpectedPresence = false }},
		{name: "document revision", mutate: func(m *AIDocumentMutation) { m.ExpectedDocumentRevision = "40" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutation := valid
			test.mutate(&mutation)
			var conflict *AIDocumentRevisionConflictError
			if err := validateCompiledFormAIDocumentMutation(state, mutation); !errors.As(err, &conflict) ||
				conflict.CurrentDocumentRevision != state.DocumentRevision || conflict.CurrentTargetRevision == nil ||
				*conflict.CurrentTargetRevision != *state.TargetRevision {
				t.Fatalf("mismatch error = %v, want document revision %q", err, state.DocumentRevision)
			}
		})
	}

	staleTarget := valid
	staleTarget.ExpectedTargetRevision = formTestStringPointer("50")
	var conflict *AIDocumentRevisionConflictError
	if err := validateCompiledFormAIDocumentMutation(state, staleTarget); !errors.As(err, &conflict) ||
		conflict.Kind != AIDocumentTargetRevisionConflict || conflict.CurrentTargetRevision == nil || *conflict.CurrentTargetRevision != "51" {
		t.Fatalf("target mismatch error = %+v", err)
	}
}

func formTestStringPointer(value string) *string { return &value }

func TestDeriveFormTargetRevisionBindsDocumentAndLocaleWriteFact(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	row := formAIDocumentLocaleRow{UpdatedAt: updatedAt}
	base, err := deriveFormTargetRevision("document-1", row)
	if err != nil {
		t.Fatal(err)
	}
	nextLocale, err := deriveFormTargetRevision(
		"document-1", formAIDocumentLocaleRow{UpdatedAt: updatedAt.Add(time.Microsecond)},
	)
	if err != nil {
		t.Fatal(err)
	}
	nextDocument, err := deriveFormTargetRevision("document-2", row)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := deriveFormTargetRevision("document-1", row)
	if err != nil {
		t.Fatal(err)
	}
	if base == nextLocale || base == nextDocument {
		t.Fatal("Form target token did not bind both document and locale write facts")
	}
	if base != stable {
		t.Fatal("same Form revision facts produced a different target token")
	}
}
