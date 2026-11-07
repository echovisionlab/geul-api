package programevent_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// compileFrom answers the AI document state with the command change makes
// of the observed one.
func compileFrom(change func(state programevent.AIDocumentState, command *programevent.AIDocumentCommand)) programevent.AIDocumentCommandCompiler {
	return func(state programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
		revision := uuid.MustParse(state.DocumentRevision)
		contributor := uuid.MustParse(state.ViewerMemberID)
		command := programevent.AIDocumentCommand{
			EventID: state.EventID, RequestedLocale: state.RequestedLocale, ObservedSourceLocale: state.SourceLocale,
			ObservedLocaleExists: state.LocaleExists, ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision,
			ContributorMemberID: contributor,
			Batch: &contentblock.Batch{
				DocumentID: state.ContentDocumentID, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor},
				LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: state.RequestedLocale}},
			},
		}
		if change != nil {
			change(state, &command)
		}
		return command, nil
	}
}

func aiAuthor(h *harness) principal {
	author := newPrincipal()
	h.store.PutMember(author.memberID)
	return author
}

func TestLoadAIDocumentStateReadsTheAuthorizedLocale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)

	state, err := h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: event.ID, Locale: "en"})

	require.NoError(t, err)
	require.Equal(t, event.ID, state.EventID)
	require.Equal(t, "Event", state.Title)
	require.Equal(t, "Source summary", *state.Summary)
	require.True(t, state.LocaleExists)
	require.Nil(t, state.TargetRevision)
	require.Equal(t, author.memberID, state.ViewerMemberID)
	require.Equal(t, h.revision(t, event).String(), state.DocumentRevision)

	target, err := h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: event.ID, Locale: "ko"})
	require.NoError(t, err)
	require.False(t, target.LocaleExists)
	require.Nil(t, target.Summary)

	_, err = h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: "not-a-uuid", Locale: "en"})
	requireCode(t, err, connect.CodeInvalidArgument, "event_id: must be a canonical UUID")
	h.store.Fence.Deactivate(author.identityID)
	_, err = h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: event.ID, Locale: "en"})
	requireCode(t, err, connect.CodeNotFound, "program event not found")
}

// An AI document names the caller's own documents, so an Author who may view an
// archived event elsewhere does not read it here.
func TestLoadAIDocumentStateOfAnArchivedEventChecksView(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	view, err := policyv1.ProgramEvent.View(event.ID)
	require.NoError(t, err)

	_, err = h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: event.ID, Locale: "en"})
	require.NoError(t, err)

	h.checker.Deny(view, nil)
	_, err = h.service.LoadAIDocumentState(author.context(t), programevent.AIDocumentStateQuery{EventID: event.ID, Locale: "en"})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to view this program event")
}

func TestExecuteAIDocumentValidationRunsThePathAndRollsItBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)
	before := h.revision(t, event)

	result, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionValidate,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Metadata = programevent.AIDocumentMetadataPatch{SetTitle: true, Title: text("Validated")}
		}),
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Equal(t, before, h.revision(t, event))
	stored, _ := h.store.StoredEvent(event.ID)
	require.Equal(t, "Event", stored.Title)
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.Updates())
}

func TestExecuteAIDocumentAppliesASourceEditAuditedToItsAuthor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)
	before := h.revision(t, event)

	result, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Metadata = programevent.AIDocumentMetadataPatch{SetTitle: true, Title: text(" AI title "), SetSummary: true, Summary: text("AI summary")}
		}),
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEqual(t, before.String(), result.DocumentRevision)
	stored, _ := h.store.StoredEvent(event.ID)
	require.Equal(t, "AI title", stored.Title)
	locale, _ := h.store.StoredLocale(event.ID, "en")
	require.Equal(t, "AI summary", *locale.Summary)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditProgramEventUpdated, records[0].Action)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	require.Equal(t, author.memberID, records[0].MemberID)
	updates := h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, programevent.SourceAI, updates[0].Source)
	require.Equal(t, []string{"title", "summary", "content"}, updates[0].Fields)
}

func TestExecuteAIDocumentRollsTheBodyBackWithFailedMetadata(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)
	before := h.revision(t, event)
	titleDown := errors.New("title write failed")
	h.store.Fail = map[string]error{"SetTitle": titleDown}

	_, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Metadata = programevent.AIDocumentMetadataPatch{SetTitle: true, Title: text("Lost")}
		}),
	})

	require.ErrorIs(t, err, titleDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Equal(t, before, h.revision(t, event))
	require.Empty(t, h.audit.Records())
}

func TestExecuteAIDocumentCreatesAndDeletesATranslation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)

	created, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "ko", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Batch, command.CreateTranslation = nil, true
		}),
	})
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.NotNil(t, created.TargetRevision)
	locale, ok := h.store.StoredLocale(event.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "Source summary", *locale.Summary, "a created translation is seeded from the source")

	deleted, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "ko", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Batch, command.DeleteTranslation = nil, true
		}),
	})
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	require.Nil(t, deleted.TargetRevision)
	_, ok = h.store.StoredLocale(event.ID, "ko")
	require.False(t, ok)

	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
		require.Equal(t, "ko", record.Locale)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationDeleted}, operations)
	updates := h.store.Updates()
	require.Len(t, updates, 2)
	require.True(t, updates[1].LocaleDeleted)

	_, err = h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.Batch, command.CreateTranslation = nil, true
		}),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "locale: source Program Event translation cannot be created or deleted")
}

func TestExecuteAIDocumentMasksWhatTheAuthorMayNotEdit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	author := aiAuthor(h)
	event := h.seedEvent(t, func(event *programevent.Event) { event.Status = programevent.StatusArchived })
	compiled := false
	_, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: func(programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
			compiled = true
			return programevent.AIDocumentCommand{}, nil
		},
	})
	requireCode(t, err, connect.CodeNotFound, "program event not found: "+event.ID)
	require.False(t, compiled, "the compiler runs only after authorization")
	editArchived, err := policyv1.ProgramEvent.EditArchived(event.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(author.identityID, editArchived, nil)}, h.checker.Checks())
}

func TestExecuteAIDocumentAnswersTheCompilerAndAMismatchedCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)
	compileErr := errors.New("operation does not compile")
	_, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: func(programevent.AIDocumentState) (programevent.AIDocumentCommand, error) {
			return programevent.AIDocumentCommand{}, compileErr
		},
	})
	require.Equal(t, compileErr, err, "the adapter receives its own compiler error")

	_, err = h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply,
		Compiler: compileFrom(func(_ programevent.AIDocumentState, command *programevent.AIDocumentCommand) {
			command.ContributorMemberID = uuid.New()
		}),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "")

	_, err = h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{EventID: event.ID, Locale: "en", Mode: 9, Compiler: compileFrom(nil)})
	requireCode(t, err, connect.CodeInvalidArgument, "mode: is not supported")
	_, err = h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{EventID: event.ID, Locale: "en"})
	requireCode(t, err, connect.CodeUnavailable, "")
}

func TestExecuteAIDocumentAnswersARevisionConflictWithTheCurrentRevision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	author := aiAuthor(h)
	event := h.seedEvent(t)
	current := h.revision(t, event)
	h.store.ContentResults = nil
	h.store.Fail = map[string]error{"ApplyContentBatch": &contentblock.StaleRevisionError{CurrentRevision: current}}

	_, err := h.service.ExecuteAIDocument(author.context(t), programevent.ExecuteAIDocumentCommand{
		EventID: event.ID, Locale: "en", Mode: programevent.AIDocumentExecutionApply, Compiler: compileFrom(nil),
	})

	var conflict *aidocument.RevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, aidocument.ConflictDocumentRevision, conflict.Kind)
	require.Equal(t, current.String(), conflict.CurrentDocumentRevision)
}
