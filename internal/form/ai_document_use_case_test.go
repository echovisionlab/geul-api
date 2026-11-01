package form_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// compileTitle edits the locale's title over the state it is shown.
func compileTitle(title string) form.AIDocumentMutationCompiler {
	return func(state form.AIDocumentState) (form.AIDocumentMutation, error) {
		return form.AIDocumentMutation{
			FormID: state.FormID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
			ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
			SetTitle: true, Title: &title, ContributorMemberID: state.ViewerMemberID,
		}, nil
	}
}

func TestLoadAIDocumentReadsTheLocaleTheCallerMayView(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	viewer := newPrincipal()

	state, err := h.service.LoadAIDocument(viewer.context(t), form.LoadAIDocumentQuery{FormID: stored.ID, Locale: "ko"})

	require.NoError(t, err)
	require.Equal(t, "en", state.SourceLocale)
	require.True(t, state.LocaleExists)
	require.Equal(t, "문의 양식", *state.LocaleTitle)
	require.Equal(t, "Contact", *state.SourceTitle)
	require.NotNil(t, state.TargetRevision)
	require.Equal(t, viewer.memberID, state.ViewerMemberID)
	require.Equal(t, h.store.DocumentRevisionOf(stored.ContentDocumentID).String(), state.DocumentRevision)

	hidden := newHarnessWith(t, &authztest.Checker{})
	other := hidden.seedForm(t, nil)
	_, err = hidden.service.LoadAIDocument(viewer.context(t), form.LoadAIDocumentQuery{FormID: other.ID, Locale: "en"})
	requireCode(t, err, connect.CodeNotFound, "form not found: "+other.ID)
	_, err = h.service.LoadAIDocument(t.Context(), form.LoadAIDocumentQuery{FormID: stored.ID, Locale: "en"})
	requireCode(t, err, connect.CodeUnauthenticated, "")
	_, err = h.service.LoadAIDocument(viewer.context(t), form.LoadAIDocumentQuery{FormID: "not-a-uuid", Locale: "en"})
	requireCode(t, err, connect.CodeInvalidArgument, "form_id: must be a canonical UUID")
}

func TestExecuteAIDocumentMutationAdvancesTheSourceAndSignalsIt(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	editor := newPrincipal()
	before := h.store.DocumentRevisionOf(stored.ContentDocumentID)

	result, err := h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "en", Mode: form.AIDocumentExecutionApply, Compile: compileTitle("Renamed"),
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Nil(t, result.TargetRevision)
	require.Equal(t, h.store.DocumentRevisionOf(stored.ContentDocumentID).String(), result.DocumentRevision)
	require.NotEqual(t, before.String(), result.DocumentRevision)
	source, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Renamed", *source.Title)
	require.Equal(t, []form.ContentUpdate{{
		FormID: stored.ID, Locale: "en", Fields: []string{"title"}, DocumentRevision: result.DocumentRevision,
		Contributor: editor.memberID, SourceLocale: true,
	}}, h.store.ContentUpdates())
	require.Equal(t, []formtest.OGRequest{{FormID: stored.ID, Locale: "en", AllLocales: true, Reason: "form_ai_document_saved"}}, h.store.OGRequests())
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, editor.memberID, records[0].MemberID)
	require.Equal(t, "en", records[0].Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
}

func TestExecuteAIDocumentMutationValidatesWithoutAWrite(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	before := h.store.DocumentRevisionOf(stored.ContentDocumentID)

	result, err := h.service.ExecuteAIDocumentMutation(newPrincipal().context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "ko", Mode: form.AIDocumentExecutionValidate, Compile: compileTitle("검증"),
	})

	require.NoError(t, err)
	require.True(t, result.Changed, "validation reports what applying would change")
	require.NotNil(t, result.TargetRevision)
	target, _ := h.store.StoredLocale(stored.ID, "ko")
	require.Equal(t, "문의 양식", *target.Title)
	require.Equal(t, before, h.store.DocumentRevisionOf(stored.ContentDocumentID))
	require.Equal(t, mutation.StepRollback, lastStep(h.units))
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.ContentUpdates())
}

func TestExecuteAIDocumentMutationCreatesAndDeletesATranslation(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	editor := newPrincipal()
	create := func(state form.AIDocumentState) (form.AIDocumentMutation, error) {
		return form.AIDocumentMutation{
			FormID: state.FormID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
			ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists, CreateTranslation: true,
			ContributorMemberID: state.ViewerMemberID,
		}, nil
	}
	created, err := h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "ja", Mode: form.AIDocumentExecutionApply, Compile: create,
	})
	require.NoError(t, err)
	require.True(t, created.Changed)
	japanese, ok := h.store.StoredLocale(stored.ID, "ja")
	require.True(t, ok)
	require.Nil(t, japanese.Title)
	require.JSONEq(t, `{"id":"source-schema","steps":[{"id":"step-1","fields":[{"id":"field-email","key":"email","type":"email","required":true}]}]}`, string(japanese.ContentJSON))

	remove := func(state form.AIDocumentState) (form.AIDocumentMutation, error) {
		return form.AIDocumentMutation{
			FormID: state.FormID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
			ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
			DeleteTranslation: true, ContributorMemberID: state.ViewerMemberID,
		}, nil
	}
	deleted, err := h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "ja", Mode: form.AIDocumentExecutionApply, Compile: remove,
	})
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	_, ok = h.store.StoredLocale(stored.ID, "ja")
	require.False(t, ok)

	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, records[1].ItemOperation)
	updates := h.store.ContentUpdates()
	require.Len(t, updates, 2)
	require.True(t, updates[1].LocaleDeleted)
	require.Equal(t, []string{"document.content"}, updates[1].Fields)
}

func TestExecuteAIDocumentMutationRejections(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	editor := newPrincipal()

	compilerErr := errors.New("compiler rejected the operation")
	_, err := h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "en", Mode: form.AIDocumentExecutionApply,
		Compile: func(form.AIDocumentState) (form.AIDocumentMutation, error) {
			return form.AIDocumentMutation{}, compilerErr
		},
	})
	require.ErrorIs(t, err, compilerErr)

	stale := func(state form.AIDocumentState) (form.AIDocumentMutation, error) {
		mutation, err := compileTitle("Stale")(state)
		mutation.ExpectedDocumentRevision = uuid.NewString()
		return mutation, err
	}
	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "en", Mode: form.AIDocumentExecutionApply, Compile: stale,
	})
	var conflict *form.AIDocumentRevisionConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, form.AIDocumentDocumentRevisionConflict, conflict.Kind)

	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: stored.ID, Locale: "en", Mode: form.AIDocumentExecutionApply, Compile: nil,
	})
	requireCode(t, err, connect.CodeUnavailable, "")

	unknown := h.seedForm(t, func(value *form.Form) { value.Status = "FORM_STATUS_ARCHIVED" })
	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: unknown.ID, Locale: "en", Mode: form.AIDocumentExecutionApply, Compile: compileTitle("Unknown"),
	})
	require.ErrorContains(t, err, "unsupported lifecycle status")

	denied := newHarnessWith(t, &authztest.Checker{})
	other := denied.seedForm(t, nil)
	_, err = denied.service.ExecuteAIDocumentMutation(editor.context(t), form.ExecuteAIDocumentMutationCommand{
		FormID: other.ID, Locale: "en", Mode: form.AIDocumentExecutionApply, Compile: compileTitle("Denied"),
	})
	requireCode(t, err, connect.CodeNotFound, "form not found: "+other.ID)
	require.Empty(t, h.audit.Records())
	require.Empty(t, denied.audit.Records())
}
