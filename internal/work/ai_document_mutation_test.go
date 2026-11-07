package work_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// compiled is the mutation of state a compiler returns, as the AI document
// adapter compiles one, changed by change.
func compiled(state work.AIDocumentState, change func(*work.AIDocumentMutation)) work.AIDocumentMutation {
	revision := uuid.MustParse(state.DocumentRevision)
	mutation := work.AIDocumentMutation{
		WorkID: state.Work.ID, Locale: state.Locale, ObservedSourceLocale: state.SourceLocale,
		ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision, ObservedLocaleExists: state.LocaleExists,
		Batch: contentblock.Batch{
			DocumentID: state.Snapshot.Document.ID, ExpectedRevision: revision,
			ContributorMemberIDs: []uuid.UUID{uuid.MustParse(state.ViewerMemberID)},
		},
		ContributorMemberID: state.ViewerMemberID,
	}
	if change != nil {
		change(&mutation)
	}
	return mutation
}

func (h *harness) execute(t *testing.T, p principal, workID, locale string, mode work.AIDocumentExecutionMode, change func(*work.AIDocumentMutation)) (work.AIDocumentMutationResult, error) {
	t.Helper()
	return h.service.ExecuteAIDocumentMutation(p.context(t), workID, locale, mode, func(state work.AIDocumentState) (work.AIDocumentMutation, error) {
		return compiled(state, change), nil
	})
}

func TestLoadAIDocumentReadsTheLocaleOfAnActiveViewer(t *testing.T) {
	h := newHarness(t)
	viewer := newPrincipal()
	value := h.seedWork(t, "Source")
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "ko", Title: stringPointer("제목")})

	state, err := h.service.LoadAIDocument(viewer.context(t), value.ID, "ko")

	require.NoError(t, err)
	require.Equal(t, value.ID, state.Work.ID)
	require.Equal(t, "en", state.SourceLocale)
	require.True(t, state.LocaleExists)
	require.Equal(t, "제목", *state.Title)
	require.NotNil(t, state.TargetRevision)
	require.Equal(t, viewer.memberID, state.ViewerMemberID)

	h.fence.Deactivate(viewer.identityID)
	_, err = h.service.LoadAIDocument(viewer.context(t), value.ID, "ko")
	requireCode(t, err, connect.CodePermissionDenied, "")
}

// An AI document names the caller's own documents, so an Author who may view an
// archived Work elsewhere does not read it here.
func TestLoadAIDocumentOfAnArchivedWorkChecksView(t *testing.T) {
	h := newHarness(t)
	viewer := newPrincipal()
	value := h.seedWork(t, "Archived", func(value *work.Work) { value.Status = work.StatusArchived })
	view, err := policyv1.Work.View(value.ID)
	require.NoError(t, err)

	_, err = h.service.LoadAIDocument(viewer.context(t), value.ID, "en")
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(viewer.identityID, view, nil)}, h.checker.Checks())

	h.checker.Deny(view, nil)
	_, err = h.service.LoadAIDocument(viewer.context(t), value.ID, "en")
	requireCode(t, err, connect.CodeNotFound, "")
}

func TestLoadAIDocumentBatchBuildsWhatASingleReadDoes(t *testing.T) {
	h := newHarness(t)
	viewer := newPrincipal()
	value := h.seedWork(t, "Source")
	other := h.seedWork(t, "Not the caller's")
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "ko", Title: stringPointer("제목")})
	single, err := h.service.LoadAIDocument(viewer.context(t), value.ID, "ko")
	require.NoError(t, err)

	states, err := h.service.LoadAIDocumentBatch(viewer.context(t), func(workID string) bool { return workID == value.ID }, []work.AIDocumentBatchRequest{
		{WorkID: value.ID, Locale: "ko"}, {WorkID: other.ID, Locale: "ko"},
	})
	require.NoError(t, err)

	batched, err := states[0]()
	require.NoError(t, err)
	require.Equal(t, single, batched)
	_, err = states[1]()
	requireCode(t, err, connect.CodeNotFound, "")
}

func TestExecuteAIDocumentMutationSavesTheSourceTitleOnce(t *testing.T) {
	h := newHarness(t)
	editor := newPrincipal()
	h.store.PutMember(editor.memberID, worktest.Member{Nickname: "Editor"})
	value := h.seedWork(t, "Source")
	setTitle := func(mutation *work.AIDocumentMutation) {
		mutation.Metadata = work.AIDocumentMetadataPatch{SetTitle: true, Title: stringPointer("AI title")}
	}

	validated, err := h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionValidate, setTitle)
	require.NoError(t, err)
	require.True(t, validated.Content.Changed)
	source, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, "Source", *source.Title, "validation rolls back")
	require.Empty(t, h.audit.Records())

	applied, err := h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionApply, setTitle)
	require.NoError(t, err)
	require.True(t, applied.Content.Changed)
	source, _ = h.store.Locale(value.ID, "en")
	require.Equal(t, "AI title", *source.Title)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkUpdated, records[0].Action)
	require.Equal(t, editor.memberID, records[0].MemberID)
	require.Equal(t, []worktest.OGRequest{{WorkID: value.ID, Locale: "en", Reason: "work_ai_document_saved"}}, h.store.OGRequests())
	require.Equal(t, []work.ContentUpdate{{WorkID: value.ID, Kind: work.InteractiveUpdate, Interactive: work.InteractiveChange{
		Locale: "en", DocumentRevision: applied.Content.DocumentRevision.String(), Changed: true, ContributorID: editor.memberID,
	}}}, h.store.Updates())
	edit, err := policyv1.Work.Edit(value.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(editor.identityID, edit, nil), authztest.Of(editor.identityID, edit, nil)}, h.checker.Checks(),
		"each execution checks Edit once")
}

func TestExecuteAIDocumentMutationAuthorizesTheLockedLifecycle(t *testing.T) {
	h := newHarness(t)
	editor := newPrincipal()
	h.store.PutMember(editor.memberID, worktest.Member{Nickname: "Editor"})
	value := h.seedWork(t, "Archived", func(value *work.Work) { value.Status = work.StatusArchived })
	editArchived, err := policyv1.Work.EditArchived(value.ID)
	require.NoError(t, err)

	_, err = h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.Metadata = work.AIDocumentMetadataPatch{SetSummary: true, Summary: stringPointer("")}
	})
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(editor.identityID, editArchived, nil)}, h.checker.Checks())

	h.checker.Deny(editArchived, nil)
	_, err = h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionApply, nil)
	requireCode(t, err, connect.CodeNotFound, "work not found: "+value.ID)
}

func TestExecuteAIDocumentMutationCreatesAndDeletesATranslationWithoutBumpingTheDocument(t *testing.T) {
	h := newHarness(t)
	editor := newPrincipal()
	h.store.PutMember(editor.memberID, worktest.Member{Nickname: "Editor"})
	value := h.seedWork(t, "Source")
	revision := h.revision(t, value.ID)

	created, err := h.execute(t, editor, value.ID, "ko", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.CreateTranslation = true
	})
	require.NoError(t, err)
	require.True(t, created.Content.Changed)
	require.NotNil(t, created.TargetRevision)
	target, ok := h.store.Locale(value.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "Source", *target.Title, "a new translation starts from the source metadata")
	require.Equal(t, revision, h.revision(t, value.ID), "a target change keeps the shared revision")

	_, err = h.execute(t, editor, value.ID, "ko", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.ExpectedTargetRevision = stringPointer("stale")
		mutation.Metadata = work.AIDocumentMetadataPatch{SetTitle: true, Title: stringPointer("Stale")}
	})
	var conflict *aidocument.RevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, aidocument.ConflictTargetRevision, conflict.Kind)
	require.Equal(t, *created.TargetRevision, *conflict.CurrentTargetRevision)

	_, err = h.execute(t, editor, value.ID, "ko", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.DeleteTranslation = true
	})
	require.NoError(t, err)
	_, ok = h.store.Locale(value.ID, "ko")
	require.False(t, ok)
	require.Equal(t, revision, h.revision(t, value.ID))
	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		operations = append(operations, record.ItemOperation)
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationDeleted}, operations)
}

func TestExecuteAIDocumentMutationAnswersAStaleDocumentAndACompilerFailure(t *testing.T) {
	h := newHarness(t)
	editor := newPrincipal()
	h.store.PutMember(editor.memberID, worktest.Member{Nickname: "Editor"})
	value := h.seedWork(t, "Source")

	_, err := h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.ExpectedRevision = uuid.New()
		mutation.Batch.ExpectedRevision = mutation.ExpectedRevision
	})
	var conflict *aidocument.RevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, aidocument.ConflictDocumentRevision, conflict.Kind)
	require.Equal(t, h.revision(t, value.ID).String(), conflict.CurrentDocumentRevision)

	compilerFailure := errors.New("operation does not compile")
	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), value.ID, "en", work.AIDocumentExecutionApply,
		func(work.AIDocumentState) (work.AIDocumentMutation, error) {
			return work.AIDocumentMutation{}, compilerFailure
		})
	require.Same(t, compilerFailure, err)

	_, err = h.execute(t, editor, value.ID, "en", work.AIDocumentExecutionApply, func(mutation *work.AIDocumentMutation) {
		mutation.CreateTranslation = true
	})
	requireCode(t, err, connect.CodeInvalidArgument, "locale: Work source translation lifecycle cannot be changed")
	require.Empty(t, h.audit.Records())
}
