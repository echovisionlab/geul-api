package legal

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewLegalAIDocumentServiceFailsClosedWithoutEveryAuthority(t *testing.T) {
	service, err := NewAuditedAIDocumentService(nil, nil, nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, service)
}

func TestLegalAIDocumentDetectsOrphanTargetValues(t *testing.T) {
	rows := []contentv1.ContentStorageRow{{Locales: []contentv1.ContentStorageLocale{{Locale: "ko"}}}}
	require.True(t, legalAIRowsContainLocale(rows, "ko"))
	require.False(t, legalAIRowsContainLocale(rows, "en"))
}

func TestCompiledLegalAIDocumentMutationMustMatchLockedState(t *testing.T) {
	entityID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	targetRevision := "tr1_legal"
	state := AIDocument{
		EntityType: "privacy", EntityID: entityID.String(), DocumentID: documentID,
		Revision: revision.String(), SourceLocale: "en", Locale: "ko",
		LocaleExists: true, TargetRevision: &targetRevision, ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentMutation{
		EntityType: "privacy", EntityID: entityID.String(), Locale: "ko",
		ExpectedRevision: revision.String(), ContributorMemberID: contributor.String(),
		ExpectedTargetRevision: &targetRevision,
		Content: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	require.NoError(t, validateCompiledLegalAIDocumentMutation(state, valid, false))

	invalid := valid
	invalid.EntityID = uuid.NewString()
	require.Error(t, validateCompiledLegalAIDocumentMutation(state, invalid, false))
	invalid = valid
	invalid.ContributorMemberID = uuid.NewString()
	require.Error(t, validateCompiledLegalAIDocumentMutation(state, invalid, false))
	invalid = valid
	invalid.Content = &contentblock.Batch{
		DocumentID: uuid.New(), ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{contributor},
	}
	require.Error(t, validateCompiledLegalAIDocumentMutation(state, invalid, false))

	authoritative := valid
	authoritative.AuthoritativeTargetReplacement = true
	require.Error(t, validateCompiledLegalAIDocumentMutation(state, authoritative, false))
	require.NoError(t, validateCompiledLegalAIDocumentMutation(state, authoritative, true))
}
