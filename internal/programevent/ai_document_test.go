package programevent

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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
				message.GetSource() != managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB {
				t.Fatalf("unexpected signal identity: %+v", message)
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

func TestProgramEventAIDocumentCompletionUsesPostCommitPublisherSemantics(t *testing.T) {
	t.Parallel()
	publisher := &programEventAIDocumentTestPublisher{err: errors.New("signal unavailable")}
	service := &ProgramEventService{asyncPublisher: publisher}
	command := AIDocumentCommand{
		EventID:              "00000000-0000-4000-8000-000000000001",
		RequestedLocale:      "en",
		ObservedSourceLocale: "en",
		ContributorMemberID:  uuid.MustParse("00000000-0000-4000-8000-000000000002"),
	}
	expected := AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003", Changed: true}
	result, err := service.completeAIDocumentApply(context.Background(), command, expected, nil)
	if err != nil || result != expected {
		t.Fatalf("committed result = %+v, %v", result, err)
	}
	if publisher.calls != 1 || publisher.signal != event.SignalContentUpdated || publisher.message == nil {
		t.Fatalf("unexpected publish call: signal=%q message=%T", publisher.signal, publisher.message)
	}
}

func TestProgramEventAIDocumentCompletionSkipsNoOpConflictAndRollback(t *testing.T) {
	t.Parallel()
	command := AIDocumentCommand{
		EventID:             "00000000-0000-4000-8000-000000000001",
		ContributorMemberID: uuid.MustParse("00000000-0000-4000-8000-000000000002"),
	}
	for _, test := range []struct {
		name      string
		result    AIDocumentResult
		commitErr error
	}{
		{name: "semantic no-op", result: AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003"}},
		{name: "revision conflict", commitErr: &AIDocumentConflict{CurrentDocumentRevision: "00000000-0000-4000-8000-000000000004"}},
		{name: "validation rollback", result: AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003", Changed: true}, commitErr: errRollbackAIDocumentValidation},
		{name: "transaction rollback", result: AIDocumentResult{DocumentRevision: "00000000-0000-4000-8000-000000000003", Changed: true}, commitErr: errors.New("transaction failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publisher := &programEventAIDocumentTestPublisher{}
			service := &ProgramEventService{asyncPublisher: publisher}
			_, err := service.completeAIDocumentApply(context.Background(), command, test.result, test.commitErr)
			if !errors.Is(err, test.commitErr) {
				t.Fatalf("completion error = %v, want %v", err, test.commitErr)
			}
			if publisher.calls != 0 || publisher.message != nil {
				t.Fatalf("negative outcome published signal=%q message=%T", publisher.signal, publisher.message)
			}
		})
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
