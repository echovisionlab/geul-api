package artist

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
)

func TestValidateCompiledArtistAIDocumentMutationKeepsLockedStateAuthoritative(t *testing.T) {
	artistID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "tr1_artist"
	state := AIDocumentState{
		ArtistID: artistID.String(), DocumentID: documentID, Revision: revision.String(),
		SourceLocale: "en", Locale: "ko", LocaleExists: true,
		TargetRevision: &targetRevision,
		ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentMutation{
		ArtistID: state.ArtistID, Locale: state.Locale, ExpectedRevision: state.Revision,
		ExpectedTargetRevision: &targetRevision,
		ExpectedSource:         state.SourceLocale, ExpectedPresence: state.LocaleExists,
		ContributorMemberID: contributor,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	resolved, err := validateCompiledArtistAIDocumentMutation(state, valid, contributor)
	if err != nil || resolved != revision {
		t.Fatalf("valid compiled mutation = (%s, %v), want %s", resolved, err, revision)
	}

	tests := []struct {
		name   string
		mutate func(*AIDocumentMutation)
	}{
		{name: "Artist", mutate: func(m *AIDocumentMutation) { m.ArtistID = uuid.NewString() }},
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
			_, err := validateCompiledArtistAIDocumentMutation(state, mutation, contributor)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("mismatched compiled mutation error = %v", err)
			}
		})
	}

	stale := valid
	stale.ExpectedRevision = uuid.NewString()
	stale.Batch = nil
	stale.DeleteTranslation = true
	_, err = validateCompiledArtistAIDocumentMutation(state, stale, contributor)
	var conflict *AIDocumentRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Kind != AIDocumentDocumentRevisionConflict || conflict.CurrentRevision != state.Revision {
		t.Fatalf("stale compiled mutation error = %v", err)
	}

	staleTarget := valid
	staleTargetRevision := "tr1_stale"
	staleTarget.ExpectedTargetRevision = &staleTargetRevision
	_, err = validateCompiledArtistAIDocumentMutation(state, staleTarget, contributor)
	conflict = nil
	if !errors.As(err, &conflict) || conflict.Kind != AIDocumentTargetRevisionConflict ||
		conflict.CurrentTargetRevision == nil || *conflict.CurrentTargetRevision != targetRevision {
		t.Fatalf("stale target compiled mutation error = %v", err)
	}
}
