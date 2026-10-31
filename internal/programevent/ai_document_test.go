package programevent

import (
	"context"
	"errors"
	"testing"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestProgramEventAIDocumentExactSeamFailsClosedWithoutDependencies(t *testing.T) {
	t.Parallel()
	service := &ProgramEventService{}
	if _, err := service.LoadAIDocumentState(context.Background(), "00000000-0000-4000-8000-000000000001", "en"); err == nil {
		t.Fatal("AI document load accepted missing Program Event dependencies")
	}
	compile := func(AIDocumentState) (AIDocumentCommand, error) { return AIDocumentCommand{}, nil }
	if _, err := service.ExecuteAIDocumentCommand(
		context.Background(),
		"00000000-0000-4000-8000-000000000001",
		"en",
		AIDocumentExecutionValidate,
		compile,
	); err == nil {
		t.Fatal("AI document validation accepted missing Program Event dependencies")
	}
	if _, err := service.ExecuteAIDocumentCommand(
		context.Background(),
		"00000000-0000-4000-8000-000000000001",
		"en",
		AIDocumentExecutionApply,
		compile,
	); err == nil {
		t.Fatal("AI document apply accepted missing Program Event dependencies")
	}
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

	for _, test := range []struct {
		name    string
		command AIDocumentCommand
	}{
		{
			name: "source block mutation",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "en",
				ObservedSourceLocale: "en", ContributorMemberID: contributorID,
			},
		},
		{
			name: "target locale value mutation",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "ko",
				ObservedSourceLocale: "en", ObservedLocaleExists: true,
				ContributorMemberID: contributorID,
			},
		},
		{
			name: "target locale creation",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "ko",
				ObservedSourceLocale: "en", CreateTranslation: true,
				ContributorMemberID: contributorID,
			},
		},
		{
			name: "target locale deletion",
			command: AIDocumentCommand{
				EventID: eventID, RequestedLocale: "ko",
				ObservedSourceLocale: "en", ObservedLocaleExists: true,
				DeleteTranslation: true, ContributorMemberID: contributorID,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			message := buildProgramEventAIDocumentContentUpdatedEvent(test.command, AIDocumentResult{
				DocumentRevision: "00000000-0000-4000-8000-000000000003", Changed: true,
			})
			if message == nil {
				t.Fatal("changed AI document did not produce a content signal")
			}
			if message.GetEntityType() != managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT ||
				message.GetEntityId() != eventID ||
				message.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI {
				t.Fatalf("unexpected signal identity: %+v", message)
			}
			if err := contentsignal.Validate(message); err != nil {
				t.Fatalf("AI document signal violates the content.updated contract: %v", err)
			}
			if len(message.GetChangedFields()) != 1 || message.GetChangedFields()[0].GetPath() != "document.content" {
				t.Fatalf("unexpected changed fields: %+v", message.GetChangedFields())
			}
			if got := message.GetContributorMemberIds(); len(got) != 1 || got[0] != contributorID.String() {
				t.Fatalf("unexpected contributors: %v", got)
			}
		})
	}
}

func TestProgramEventAIDocumentContentSignalSkipsNoOp(t *testing.T) {
	t.Parallel()
	message := buildProgramEventAIDocumentContentUpdatedEvent(AIDocumentCommand{
		EventID:             "00000000-0000-4000-8000-000000000001",
		ContributorMemberID: uuid.MustParse("00000000-0000-4000-8000-000000000002"),
	}, AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003"})
	if message != nil {
		t.Fatalf("no-op AI document produced a content signal: %+v", message)
	}
}

func TestProgramEventAIDocumentContentSignalCarriesExactLocaleTuple(t *testing.T) {
	t.Parallel()
	contributorID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	targetRevision := "target-revision"
	event := buildProgramEventAIDocumentContentUpdatedEvent(
		AIDocumentCommand{
			EventID:              "00000000-0000-4000-8000-000000000001",
			RequestedLocale:      "ko",
			ObservedSourceLocale: "en",
			ContributorMemberID:  contributorID,
		},
		AIDocumentResult{
			DocumentRevision: "document-revision", TargetRevision: &targetRevision, Changed: true,
		},
	)
	if event == nil {
		t.Fatal("target mutation did not produce a content signal")
	}
	if event.GetLocale() != "ko" || !event.GetLocaleExists() || event.GetTargetRevision() != targetRevision {
		t.Fatalf("unexpected target locale tuple: %+v", event)
	}
	if event.GetDocumentStateChanged() || event.GetDocumentRevision() != "document-revision" {
		t.Fatalf("target mutation incorrectly changed shared document state: %+v", event)
	}
	source := buildProgramEventAIDocumentContentUpdatedEvent(
		AIDocumentCommand{
			EventID:         "00000000-0000-4000-8000-000000000001",
			RequestedLocale: "en", ObservedSourceLocale: "en", ContributorMemberID: contributorID,
		},
		AIDocumentResult{DocumentRevision: "source-revision", Changed: true},
	)
	if source == nil || source.GetLocale() != "en" || !source.GetLocaleExists() || !source.GetDocumentStateChanged() {
		t.Fatalf("unexpected source locale tuple: %+v", source)
	}
	if source.GetTargetRevision() != "" {
		t.Fatalf("source mutation carried target state: %+v", source)
	}
}

func TestProgramEventAIDocumentContentSignalRequiresOwningTransaction(t *testing.T) {
	t.Parallel()
	publisher := &programEventAIDocumentTestPublisher{}
	command := AIDocumentCommand{
		EventID:              "00000000-0000-4000-8000-000000000001",
		RequestedLocale:      "en",
		ObservedSourceLocale: "en",
		ContributorMemberID:  uuid.MustParse("00000000-0000-4000-8000-000000000002"),
	}
	changed := buildProgramEventAIDocumentContentUpdatedEvent(
		command,
		AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003", Changed: true},
	)
	require.NotNil(t, changed)
	require.Error(t, publishContentUpdatedEvent(context.Background(), nil, publisher, changed))
	require.Error(t, publishContentUpdatedEvent(context.Background(), &gorm.DB{}, publisher, changed))
	require.Zero(t, publisher.calls, "content.updated escaped the owning transaction")

	noop := buildProgramEventAIDocumentContentUpdatedEvent(
		command,
		AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003"},
	)
	require.Nil(t, noop)
	require.NoError(t, publishContentUpdatedEvent(context.Background(), nil, publisher, noop))
	require.Zero(t, publisher.calls)
}

func TestProgramEventContentSignalsSatisfyContract(t *testing.T) {
	t.Parallel()
	eventID := "00000000-0000-4000-8000-000000000001"
	contributor := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	documentRevision := "00000000-0000-4000-8000-000000000003"
	targetRevision := "target-revision"
	deleted := buildProgramEventAIDocumentContentUpdatedEvent(
		AIDocumentCommand{
			EventID: eventID, RequestedLocale: "ko", ObservedSourceLocale: "en",
			ObservedLocaleExists: true, DeleteTranslation: true, ContributorMemberID: contributor,
		},
		AIDocumentResult{DocumentRevision: documentRevision, TargetRevision: &targetRevision, Changed: true},
	)
	require.NotNil(t, deleted, "translation deletion must signal content.updated")
	require.NoError(t, contentsignal.Validate(deleted))
	require.False(t, deleted.GetLocaleExists())
	require.Nil(t, deleted.TargetRevision)
	require.Equal(t, documentRevision, deleted.GetDocumentRevision())

	for _, collab := range []*managev1.ContentUpdatedEvent{
		buildProgramEventLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:             eventID,
			Fields:               []string{"content"},
			DocumentRevision:     documentRevision,
			Contributors:         []string{contributor.String()},
			Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			Locale:               "en",
			DocumentStateChanged: true,
		}),
		buildProgramEventLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:         eventID,
			Fields:           []string{"summary"},
			DocumentRevision: documentRevision,
			Contributors:     []string{contributor.String()},
			Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			Locale:           "ko",
			TargetRevision:   &targetRevision,
		}),
	} {
		require.NotNil(t, collab)
		require.Equal(t, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB, collab.GetSource())
		require.NoError(t, contentsignal.Validate(collab))
	}
}

type programEventAIDocumentTestPublisher struct {
	calls   int
	signal  string
	message proto.Message
	err     error
}

func (publisher *programEventAIDocumentTestPublisher) NotifyProtobuf(_ context.Context, signal string, message proto.Message) error {
	publisher.calls++
	publisher.signal = signal
	publisher.message = message
	return publisher.err
}

func (*programEventAIDocumentTestPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
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
	signal := buildProgramEventAIDocumentContentUpdatedEvent(command, result)
	require.NotNil(t, signal)
	fields := signal.GetChangedFields()
	require.Len(t, fields, 2)
	require.Equal(t, "title", fields[0].GetPath())
	require.Equal(t, "summary", fields[1].GetPath())
	command.Batch.Upserts = []contentblock.BaseBlock{{}}
	signal = buildProgramEventAIDocumentContentUpdatedEvent(command, result)
	require.Len(t, signal.GetChangedFields(), 3)
	require.Equal(t, "document.content", signal.GetChangedFields()[2].GetPath())
	result.Changed = false
	require.Nil(t, buildProgramEventAIDocumentContentUpdatedEvent(command, result))
}

func (publisher *programEventAIDocumentTestPublisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return publisher.NotifyProtobuf(ctx, signal, message)
}
