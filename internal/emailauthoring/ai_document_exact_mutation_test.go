package emailauthoring

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

func TestCompiledEmailTemplateAIDocumentMutationBindsLockedState(t *testing.T) {
	t.Parallel()
	documentID := uuid.New()
	revision := uuid.New()
	memberID := uuid.New()
	state := AIDocumentState{
		TemplateID: "11111111-1111-4111-8111-111111111111", DocumentID: documentID,
		DocumentRevision: revision.String(), SourceLocale: "ko", Locale: "en",
		LocaleExists: true, ViewerMemberID: memberID.String(),
	}
	mutation := AIDocumentMutation{
		TemplateID: state.TemplateID, Locale: state.Locale,
		ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
		ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{memberID},
		},
	}
	require.NoError(t, validateCompiledEmailTemplateAIDocumentMutation(state, mutation, revision))

	stale := mutation
	stale.ExpectedPresence = false
	var conflict *AIDocumentRevisionConflictError
	require.ErrorAs(t, validateCompiledEmailTemplateAIDocumentMutation(state, stale, revision), &conflict)
	require.Equal(t, state.DocumentRevision, conflict.CurrentDocumentRevision)

	wrongContributor := mutation
	wrongContributor.ContributorMember = uuid.New()
	require.Error(t, validateCompiledEmailTemplateAIDocumentMutation(state, wrongContributor, revision))

	wrongDocument := mutation
	wrongDocument.Batch = &contentblock.Batch{
		DocumentID: uuid.New(), ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{memberID},
	}
	err := validateCompiledEmailTemplateAIDocumentMutation(state, wrongDocument, revision)
	require.Error(t, err)
	require.False(t, errors.As(err, &conflict))
}
