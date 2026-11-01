//go:build integration

package programevent_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/programevent"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProgramEventAIDocumentMetadataOwningTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(ctx).MemberID.String()
	suffix := testutil.IntegrationUUID()
	eventType, err := typeHandlerForTest(t, db, spiceDB).CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{Slug: "aidocument-metadata-type-" + suffix, Locale: "en", Name: "Metadata test", RequiresPlace: ptrBool(false), RequiresStreamUrl: ptrBool(false)}))
	require.NoError(t, err)
	publisher := &programEventAIDocumentTestPublisher{}
	service := newIntegrationService(t, db, spiceDB, integrationDeps{Signals: publisher})
	created, err := programevent.NewConnectHandler(service).CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{Title: "Source event title", Slug: "aidocument-metadata-event-" + suffix, SourceLocale: "en", TypeId: eventType.Msg.Id, StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Timezone: "Asia/Seoul", LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE, Summary: ptrString("Source summary")}))
	require.NoError(t, err)
	eventID := created.Msg.Id
	initial, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "en"})
	require.NoError(t, err)
	require.Equal(t, "Source event title", initial.Title)
	require.Equal(t, "Source summary", *initial.Summary)
	blockID := testutil.IntegrationUUID()
	compile := func(state programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
		batch, err := contentblock.BatchFromRichTextProto(state.ContentDocumentID, programEventParagraphMutationBatch(state.LocalizedDocument, state.DocumentRevision, blockID, "en", "New source body", []string{memberID}, true))
		if err != nil {
			return programevent.AIDocumentCommand{}, err
		}
		command := programEventMetadataCommandForState(state)
		command.Batch = &batch
		command.Metadata = programevent.AIDocumentMetadataPatch{SetTitle: true, Title: ptrString("Updated source title"), SetSummary: true, Summary: ptrString("Updated source summary")}
		return command, nil
	}
	// Exact validation performs the body and metadata mutation and rolls it all back.
	validated, err := service.ExecuteAIDocument(ctx, programevent.ExecuteAIDocumentCommand{EventID: eventID, Locale: "en", Mode: programevent.AIDocumentExecutionValidate, Compiler: compile})
	require.NoError(t, err)
	require.True(t, validated.Changed)
	afterValidate, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "en"})
	require.NoError(t, err)
	require.Equal(t, initial.DocumentRevision, afterValidate.DocumentRevision)
	require.Equal(t, initial.Title, afterValidate.Title)
	require.Equal(t, *initial.Summary, *afterValidate.Summary)
	require.Empty(t, afterValidate.LocalizedDocument.Base.Nodes)
	require.Zero(t, publisher.calls)
	// A metadata failure must not leave the paired body or the summary stored.
	_, err = service.ExecuteAIDocument(ctx, programevent.ExecuteAIDocumentCommand{EventID: eventID, Locale: "en", Mode: programevent.AIDocumentExecutionApply, Compiler: func(state programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
		command, err := compile(state)
		command.Metadata.Title = ptrString(" ")
		return command, err
	}})
	require.Error(t, err)
	afterFailure, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "en"})
	require.NoError(t, err)
	require.Equal(t, initial.DocumentRevision, afterFailure.DocumentRevision)
	require.Equal(t, initial.Title, afterFailure.Title)
	require.Equal(t, *initial.Summary, *afterFailure.Summary)
	require.Empty(t, afterFailure.LocalizedDocument.Base.Nodes)
	require.Zero(t, publisher.calls)
	applied, err := service.ExecuteAIDocument(ctx, programevent.ExecuteAIDocumentCommand{EventID: eventID, Locale: "en", Mode: programevent.AIDocumentExecutionApply, Compiler: compile})
	require.NoError(t, err)
	require.True(t, applied.Changed)
	source, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "en"})
	require.NoError(t, err)
	require.Equal(t, "Updated source title", source.Title)
	require.Equal(t, "Updated source summary", *source.Summary)
	require.Len(t, source.LocalizedDocument.Base.Nodes, 1)
	require.Equal(t, "New source body", source.LocalizedDocument.LocaleOverlay.Blocks[0].GetParagraph().Content[0].GetText().Text)
	require.NotEqual(t, initial.DocumentRevision, source.DocumentRevision)
	// The target bootstrap keeps the source-owned title and seeds the existing body.
	absent, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "ko"})
	require.NoError(t, err)
	require.False(t, absent.LocaleExists)
	require.Nil(t, absent.Summary, "an absent target must not project source-summary fallback")
	targetResult, err := service.ExecuteAIDocument(ctx, programevent.ExecuteAIDocumentCommand{EventID: eventID, Locale: "ko", Mode: programevent.AIDocumentExecutionApply, Compiler: func(state programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
		command := programEventMetadataCommandForState(state)
		command.Metadata = programevent.AIDocumentMetadataPatch{SetSummary: true, Summary: ptrString("번역 요약")}
		return command, nil
	}})
	require.NoError(t, err)
	require.True(t, targetResult.Changed)
	require.Equal(t, source.DocumentRevision, targetResult.DocumentRevision)
	require.NotNil(t, targetResult.TargetRevision)
	target, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "ko"})
	require.NoError(t, err)
	require.Equal(t, source.Title, target.Title)
	require.Equal(t, "번역 요약", *target.Summary)
	require.True(t, target.LocaleExists)
	require.Len(t, target.LocalizedDocument.LocaleOverlay.Blocks, 1)
	// The native target seam supports nullable clear without manufacturing a title.
	cleared, err := service.ExecuteAIDocument(ctx, programevent.ExecuteAIDocumentCommand{EventID: eventID, Locale: "ko", Mode: programevent.AIDocumentExecutionApply, Compiler: func(state programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
		command := programEventMetadataCommandForState(state)
		command.Metadata = programevent.AIDocumentMetadataPatch{SetSummary: true}
		return command, nil
	}})
	require.NoError(t, err)
	require.True(t, cleared.Changed)
	require.NotEqual(t, *target.TargetRevision, *cleared.TargetRevision)
	clearedState, err := service.LoadAIDocumentState(ctx, programevent.AIDocumentStateQuery{EventID: eventID, Locale: "ko"})
	require.NoError(t, err)
	require.Nil(t, clearedState.Summary)
	require.Equal(t, source.Title, clearedState.Title)
}

func programEventMetadataCommandForState(state programevent.AIDocumentState) programevent.AIDocumentCommand {
	revision, member := uuid.MustParse(state.DocumentRevision), uuid.MustParse(state.ViewerMemberID)
	return programevent.AIDocumentCommand{EventID: state.EventID, RequestedLocale: state.RequestedLocale, ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists, ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision, ContributorMemberID: member,
		Batch: &contentblock.Batch{DocumentID: state.ContentDocumentID, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{member}},
	}
}
