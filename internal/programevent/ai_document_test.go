package programevent

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The AI document seam and every other use case fail at construction, not
// at request time, when a collaborator is missing.
func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	t.Parallel()
	_, err := NewService(Deps{})
	require.ErrorContains(t, err, "program event dependencies are required: Audit, CreditMembers, Fence, SpiceDB, Units")
}

func TestProgramEventAIDocumentConflictCarriesCurrentRevision(t *testing.T) {
	t.Parallel()
	err := &AIDocumentConflict{CurrentDocumentRevision: "00000000-0000-4000-8000-000000000001"}
	var conflict *AIDocumentConflict
	if !errors.As(err, &conflict) || conflict.CurrentDocumentRevision == "" {
		t.Fatalf("conflict lost revision: %#v", err)
	}
}

func TestCompiledProgramEventAIDocumentCommandMustMatchLockedState(t *testing.T) {
	t.Parallel()
	eventID, documentID, revision, contributor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	state := AIDocumentState{
		EventID: eventID.String(), ContentDocumentID: documentID,
		DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "ko",
		LocaleExists: true, ViewerMemberID: contributor.String(),
	}
	valid := AIDocumentCommand{
		EventID: eventID.String(), RequestedLocale: "ko", ObservedSourceLocale: "en",
		ObservedLocaleExists: true, ExpectedRevision: revision, ContributorMemberID: contributor,
		Batch: &contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
	}
	require.NoError(t, validateCompiledProgramEventAIDocumentCommand(state, valid))

	invalid := valid
	invalid.EventID = uuid.NewString()
	require.Error(t, validateCompiledProgramEventAIDocumentCommand(state, invalid))
	invalid = valid
	invalid.ContributorMemberID = uuid.New()
	require.Error(t, validateCompiledProgramEventAIDocumentCommand(state, invalid))
	invalid = valid
	invalid.Batch = &contentblock.Batch{
		DocumentID: uuid.New(), ExpectedRevision: revision,
		ContributorMemberIDs: []uuid.UUID{contributor},
	}
	require.Error(t, validateCompiledProgramEventAIDocumentCommand(state, invalid))
}

func TestProgramEventAIDocumentContentSignalCoversSourceAndTargetChanges(t *testing.T) {
	t.Parallel()
	eventID := "00000000-0000-4000-8000-000000000001"
	contributorID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	revision := "00000000-0000-4000-8000-000000000003"
	targetRevision := "target-revision"

	for _, test := range []struct {
		name    string
		command AIDocumentCommand
		result  AIDocumentResult
		want    ContentUpdate
	}{
		{
			name: "source block mutation",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "en",
				ObservedSourceLocale: "en", ContributorMemberID: contributorID,
			},
			result: AIDocumentResult{DocumentRevision: revision, Changed: true},
			want: ContentUpdate{
				EventID: eventID, Fields: []string{"content"}, DocumentRevision: revision,
				Contributors: []string{contributorID.String()}, Source: SourceAI, Locale: "en", DocumentStateChanged: true,
			},
		},
		{
			name: "target locale value mutation",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "ko",
				ObservedSourceLocale: "en", ObservedLocaleExists: true,
				ContributorMemberID: contributorID,
			},
			result: AIDocumentResult{DocumentRevision: revision, TargetRevision: &targetRevision, Changed: true},
			want: ContentUpdate{
				EventID: eventID, Fields: []string{"content"}, DocumentRevision: revision,
				Contributors: []string{contributorID.String()}, Source: SourceAI, Locale: "ko", TargetRevision: &targetRevision,
			},
		},
		{
			name: "target locale deletion",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "ko",
				ObservedSourceLocale: "en", ObservedLocaleExists: true,
				DeleteTranslation: true, ContributorMemberID: contributorID,
			},
			result: AIDocumentResult{DocumentRevision: revision, Changed: true},
			want: ContentUpdate{
				EventID: eventID, Fields: []string{"content"}, DocumentRevision: revision,
				Contributors: []string{contributorID.String()}, Source: SourceAI, Locale: "ko", LocaleDeleted: true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, aiDocumentContentUpdate(test.command, test.result))
		})
	}
}

func TestProgramEventMetadataCompiledTitleRemainsSourceOwned(t *testing.T) {
	contributor, documentID, revision := uuid.New(), uuid.New(), uuid.New()
	state := AIDocumentState{EventID: uuid.NewString(), ContentDocumentID: documentID, DocumentRevision: revision.String(), RequestedLocale: "ko", SourceLocale: "en", LocaleExists: true, ViewerMemberID: contributor.String()}
	title, summary := "invalid target title", ""
	command := AIDocumentCommand{EventID: state.EventID, RequestedLocale: state.RequestedLocale, ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: true, ExpectedRevision: revision, ContributorMemberID: contributor, Metadata: AIDocumentMetadataPatch{SetTitle: true, Title: &title}}
	require.Error(t, validateCompiledProgramEventAIDocumentCommand(state, command))
	command.Metadata = AIDocumentMetadataPatch{SetSummary: true, Summary: &summary}
	require.NoError(t, validateCompiledProgramEventAIDocumentCommand(state, command))
}

func TestProgramEventMetadataContentSignalIncludesMetadataAndCombinedBody(t *testing.T) {
	command := AIDocumentCommand{EventID: uuid.NewString(), RequestedLocale: "en", ObservedSourceLocale: "en", ContributorMemberID: uuid.New(), Metadata: AIDocumentMetadataPatch{SetTitle: true, SetSummary: true}, Batch: &contentblock.Batch{}}
	result := AIDocumentResult{DocumentRevision: uuid.NewString(), Changed: true}
	require.Equal(t, []string{"title", "summary"}, aiDocumentContentUpdate(command, result).Fields)
	command.Batch.Upserts = []contentblock.BaseBlock{{}}
	require.Equal(t, []string{"title", "summary", "content"}, aiDocumentContentUpdate(command, result).Fields)
}
