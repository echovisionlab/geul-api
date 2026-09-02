package campaign

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

func TestCompiledCampaignAIDocumentMutationBindsLockedState(t *testing.T) {
	t.Parallel()
	documentID := uuid.New()
	revision := uuid.New()
	memberID := uuid.New()
	state := AIDocumentState{
		CampaignID: "33333333-3333-4333-8333-333333333333", Status: "CAMPAIGN_STATUS_DRAFT",
		DocumentID: documentID, DocumentRevision: revision.String(), SourceLocale: "ko", Locale: "en",
		LocaleExists: true, ViewerMemberID: memberID.String(),
	}
	mutation := AIDocumentMutation{
		CampaignID: state.CampaignID, Locale: state.Locale,
		ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
		ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{memberID},
		},
	}
	require.NoError(t, validateCompiledCampaignAIDocumentMutation(state, mutation, revision))

	stale := mutation
	stale.ExpectedSource = "fr"
	var conflict *AIDocumentRevisionConflictError
	require.ErrorAs(t, validateCompiledCampaignAIDocumentMutation(state, stale, revision), &conflict)
	require.Equal(t, state.DocumentRevision, conflict.CurrentDocumentRevision)

	wrongDocument := mutation
	wrongDocument.Batch = &contentblock.Batch{
		DocumentID: uuid.New(), ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{memberID},
	}
	require.Error(t, validateCompiledCampaignAIDocumentMutation(state, wrongDocument, revision))
}
