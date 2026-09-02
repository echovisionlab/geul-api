package emailauthoring

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompiledEmailLayoutAIDocumentMutationBindsLockedState(t *testing.T) {
	t.Parallel()
	memberID := uuid.New()
	targetRevision := "tr1_target"
	state := EmailLayoutAIDocumentState{
		LayoutID:         "22222222-2222-4222-8222-222222222222",
		DocumentRevision: "eld1_revision", TargetRevision: &targetRevision,
		SourceLocale: "ko", Locale: "en", LocaleExists: true,
		ViewerMemberID: memberID.String(),
	}
	mutation := EmailLayoutAIDocumentMutation{
		LayoutID: state.LayoutID, Locale: state.Locale,
		ExpectedDocumentRevision: state.DocumentRevision,
		ExpectedTargetRevision:   state.TargetRevision, ExpectedSource: state.SourceLocale,
		ExpectedPresence: state.LocaleExists, ContributorMemberID: memberID, Noop: true,
	}
	require.NoError(t, validateCompiledEmailLayoutAIDocumentMutation(state, mutation))

	stale := mutation
	stale.ExpectedDocumentRevision = "eld1_stale"
	var conflict *EmailLayoutAIDocumentRevisionConflictError
	require.ErrorAs(t, validateCompiledEmailLayoutAIDocumentMutation(state, stale), &conflict)
	require.Equal(t, EmailLayoutAIDocumentDocumentRevisionConflict, conflict.Kind)
	require.Equal(t, state.DocumentRevision, conflict.CurrentDocumentRevision)
	require.Equal(t, state.TargetRevision, conflict.CurrentTargetRevision)

	staleTarget := mutation
	staleTargetRevision := "tr1_stale"
	staleTarget.ExpectedTargetRevision = &staleTargetRevision
	require.ErrorAs(t, validateCompiledEmailLayoutAIDocumentMutation(state, staleTarget), &conflict)
	require.Equal(t, EmailLayoutAIDocumentTargetRevisionConflict, conflict.Kind)
	require.Equal(t, state.DocumentRevision, conflict.CurrentDocumentRevision)
	require.Equal(t, state.TargetRevision, conflict.CurrentTargetRevision)

	wrongContributor := mutation
	wrongContributor.ContributorMemberID = uuid.New()
	require.Error(t, validateCompiledEmailLayoutAIDocumentMutation(state, wrongContributor))
}

func TestEmailLayoutAIDocumentCompilerStateCannotMutateLockedObservation(t *testing.T) {
	t.Parallel()
	value := "locked"
	state := EmailLayoutAIDocumentState{
		Units: []EmailLayoutAIDocumentUnit{{Handle: "text:1", LocaleValue: &value}},
	}
	compilerState := cloneEmailLayoutAIDocumentState(state)
	compilerState.Units[0].Handle = "changed"
	*compilerState.Units[0].LocaleValue = "changed"

	require.Equal(t, "text:1", state.Units[0].Handle)
	require.Equal(t, "locked", *state.Units[0].LocaleValue)
}
