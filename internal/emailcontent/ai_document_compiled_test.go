package emailcontent

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompiledAIDocumentMutationBindsLockedState(t *testing.T) {
	t.Parallel()
	documentID := uuid.New()
	revision := uuid.New()
	memberID := uuid.New()
	state := AIDocumentState{
		Reference: "33333333-3333-4333-8333-333333333333", DocumentID: documentID,
		DocumentRevision: revision.String(), SourceLocale: "ko", Locale: "en",
		LocaleExists: true, ViewerMemberID: memberID.String(),
	}
	mutation := AIDocumentMutation{
		Reference: state.Reference, Locale: state.Locale,
		ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
		ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{memberID},
		},
	}
	require.NoError(t, testOwner.validateCompiledAIDocumentMutation(state, mutation, revision))

	for _, stale := range []AIDocumentMutation{
		func() AIDocumentMutation { value := mutation; value.ExpectedSource = "fr"; return value }(),
		func() AIDocumentMutation { value := mutation; value.ExpectedPresence = false; return value }(),
	} {
		var conflict *aidocument.RevisionConflict
		require.ErrorAs(t, testOwner.validateCompiledAIDocumentMutation(state, stale, revision), &conflict)
		require.Equal(t, state.DocumentRevision, conflict.CurrentDocumentRevision)
	}

	wrongContributor := mutation
	wrongContributor.ContributorMember = uuid.New()
	require.Error(t, testOwner.validateCompiledAIDocumentMutation(state, wrongContributor, revision))

	wrongDocument := mutation
	wrongDocument.Batch = &contentblock.Batch{
		DocumentID: uuid.New(), ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{memberID},
	}
	err := testOwner.validateCompiledAIDocumentMutation(state, wrongDocument, revision)
	require.Error(t, err)
	var conflict *aidocument.RevisionConflict
	require.False(t, errors.As(err, &conflict))
}
