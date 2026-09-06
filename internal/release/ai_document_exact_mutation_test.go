package release

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
)

func TestExecuteAIDocumentMutationRequiresExactCanonicalLocale(t *testing.T) {
	for _, locale := range []string{" ko", "ko ", "KO", "ko-KR", "ko_kr", "zh-Hant", "es-MX", "pt", ""} {
		if _, err := normalizeReleaseDocumentLocale(locale); err == nil {
			t.Fatalf("non-exact locale %q passed exact canonical validation", locale)
		}
	}
	if normalized, err := normalizeReleaseDocumentLocale("ko"); err != nil || normalized != "ko" {
		t.Fatalf("canonical locale rejected: %q %v", normalized, err)
	}
}

func TestValidateCompiledReleaseAIDocumentMutationKeepsLockedStateAuthoritative(t *testing.T) {
	releaseID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "tr1_target"
	state := AIDocumentState{
		ReleaseID: releaseID.String(), DocumentID: documentID, DocumentRevision: revision.String(),
		SourceLocale: "en", Locale: "ko", LocaleExists: true, TargetRevision: &targetRevision,
		ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentMutation{
		ReleaseID: state.ReleaseID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
		ExpectedTargetRevision: &targetRevision,
		ExpectedSource:         state.SourceLocale, ExpectedPresence: state.LocaleExists,
		ContributorMemberID: contributor,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	resolved, err := validateCompiledReleaseAIDocumentMutation(state, valid, contributor)
	if err != nil || resolved != revision {
		t.Fatalf("valid compiled mutation = (%s, %v), want %s", resolved, err, revision)
	}

	tests := []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "Release", mutate: func(m *AIDocumentMutation) { m.ReleaseID = uuid.NewString() }},
		{name: "locale", mutate: func(m *AIDocumentMutation) { m.Locale = "fr" }},
		{name: "source", mutate: func(m *AIDocumentMutation) { m.ExpectedSource = "fr" }},
		{name: "presence", mutate: func(m *AIDocumentMutation) { m.ExpectedPresence = false }},
		{name: "contributor", mutate: func(m *AIDocumentMutation) { m.ContributorMemberID = uuid.New() }},
		{name: "document", mutate: func(m *AIDocumentMutation) { m.Batch.DocumentID = uuid.New() }},
		{name: "batch revision", mutate: func(m *AIDocumentMutation) { m.Batch.ExpectedRevision = uuid.New() }},
		{name: "batch contributor", mutate: func(m *AIDocumentMutation) { m.Batch.ContributorMemberIDs = []uuid.UUID{uuid.New()} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutation := valid
			batch := *valid.Batch
			batch.ContributorMemberIDs = append([]uuid.UUID(nil), valid.Batch.ContributorMemberIDs...)
			mutation.Batch = &batch
			test.mutate(&mutation)
			_, err := validateCompiledReleaseAIDocumentMutation(state, mutation, contributor)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("mismatched compiled mutation error = %v", err)
			}
		})
	}

	stale := valid
	stale.ExpectedDocumentRevision = uuid.NewString()
	stale.Batch = nil
	stale.CreditNotePatch = map[string]string{uuid.NewString(): "note"}
	_, err = validateCompiledReleaseAIDocumentMutation(state, stale, contributor)
	var conflict *AIDocumentRevisionConflictError
	if !errors.As(err, &conflict) || conflict.CurrentDocumentRevision != state.DocumentRevision {
		t.Fatalf("stale compiled mutation error = %v", err)
	}

	staleTarget := valid
	otherTarget := "tr1_other"
	staleTarget.ExpectedTargetRevision = &otherTarget
	_, err = validateCompiledReleaseAIDocumentMutation(state, staleTarget, contributor)
	if !errors.As(err, &conflict) || conflict.Kind != AIDocumentTargetRevisionConflict || conflict.CurrentTargetRevision == nil || *conflict.CurrentTargetRevision != targetRevision {
		t.Fatalf("stale target mutation error = %v", err)
	}
}
