package page_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// metadataCompiler compiles a patch of the locked state's metadata, as the
// AI document adapter does, and counts its calls.
type metadataCompiler struct {
	patch page.AIDocumentMetadataPatch
	calls int
	err   error
}

func (c *metadataCompiler) compile(state page.AIDocumentState) (page.AIDocumentMutation, error) {
	c.calls++
	if c.err != nil {
		return page.AIDocumentMutation{}, c.err
	}
	revision := uuid.MustParse(state.Revision)
	contributor := uuid.MustParse(state.ViewerMemberID)
	return page.AIDocumentMutation{
		PageID: state.Page.ID, Locale: state.Locale, ObservedSourceLocale: state.SourceLocale,
		ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision, ObservedLocaleExists: state.LocaleExists,
		ContributorMemberID: state.ViewerMemberID, Metadata: c.patch,
		Batch: contentblock.Batch{DocumentID: state.Snapshot.Document.ID, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor}},
	}, nil
}

func TestLoadAIDocumentReadsTheLockedLocaleForAViewer(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	h.store.PutLocale(stored.ID, page.Locale{Locale: "ko", Title: text("번역")})
	reader := h.member(newPrincipal())

	state, err := h.service.LoadAIDocument(reader.context(t), page.LoadAIDocumentQuery{PageID: stored.ID, Locale: "ko"})
	require.NoError(t, err)
	require.Equal(t, "en", state.SourceLocale)
	require.True(t, state.LocaleExists)
	require.Equal(t, "번역", *state.Title)
	require.NotNil(t, state.TargetRevision)
	require.Equal(t, reader.memberID, state.ViewerMemberID)
	require.Empty(t, h.audit.Records())

	view, err := policyv1.Page.View(stored.ID)
	require.NoError(t, err)
	h.checker.Deny(view, nil)
	_, err = h.service.LoadAIDocument(reader.context(t), page.LoadAIDocumentQuery{PageID: stored.ID, Locale: "ko"})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestLoadAIDocumentBatchBuildsWhatASingleReadDoes(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	other := h.seedPage("Not the caller's")
	h.store.PutLocale(stored.ID, page.Locale{Locale: "ko", Title: text("번역")})
	reader := h.member(newPrincipal())
	single, err := h.service.LoadAIDocument(reader.context(t), page.LoadAIDocumentQuery{PageID: stored.ID, Locale: "ko"})
	require.NoError(t, err)

	states, err := h.service.LoadAIDocumentBatch(reader.context(t), func(pageID string) bool { return pageID == stored.ID }, []page.LoadAIDocumentQuery{
		{PageID: stored.ID, Locale: "ko"}, {PageID: other.ID, Locale: "ko"},
	})
	require.NoError(t, err)

	batched, err := states[0]()
	require.NoError(t, err)
	require.Equal(t, single, batched)
	_, err = states[1]()
	requireCode(t, err, connect.CodeNotFound, "page not found: "+other.ID)
}

func TestExecuteAIDocumentMutationAuthorizesBeforeCompilingOnce(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	editor := h.member(newPrincipal())
	edit, err := policyv1.Page.Edit(stored.ID)
	require.NoError(t, err)
	h.checker.For(editor.identityID).Deny(edit, nil)
	compiler := &metadataCompiler{patch: page.AIDocumentMetadataPatch{SetTitle: true, Title: text("Edited")}}

	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionApply, Compile: compiler.compile,
	})

	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
	require.Zero(t, compiler.calls, "a caller who may not edit never reaches the compiler")
}

func TestExecuteAIDocumentMutationValidatesWithoutPersisting(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	editor := h.member(newPrincipal())
	compiler := &metadataCompiler{patch: page.AIDocumentMetadataPatch{SetTitle: true, Title: text("Edited")}}

	result, err := h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionValidate, Compile: compiler.compile,
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, 1, compiler.calls)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	locale, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Source", *locale.Title)
	require.Empty(t, h.store.Updates())
	require.Empty(t, h.audit.Records())
}

func TestExecuteAIDocumentMutationAppliesASourceTitleAndLayout(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	editor := h.member(newPrincipal())
	revision := h.revision(t, stored.ID)
	layout := page.DocumentLayout{ContentHeight: page.ContentHeightViewport, PageChrome: page.PlacementPinned, Footer: page.PlacementPinned}
	compiler := &metadataCompiler{patch: page.AIDocumentMetadataPatch{
		SetTitle: true, Title: text("Edited"), SetDocumentLayout: true, DocumentLayout: layout,
	}}

	result, err := h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionApply, Compile: compiler.compile,
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEqual(t, revision.String(), result.DocumentRevision)
	locale, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Edited", *locale.Title)
	updated, _ := h.store.Page(stored.ID)
	require.Equal(t, layout, updated.DocumentLayout)
	require.Equal(t, []pagetest.OGRequest{{PageID: stored.ID, Locale: "en", Reason: "page_ai_document_saved"}}, h.store.OGRequests())
	require.Equal(t, page.LocaleUpdateChange{
		Fields: []string{"title", "documentLayout"}, DocumentRevision: result.DocumentRevision,
		Contributors: []string{editor.memberID}, Source: page.SourceAI, Locale: "en", DocumentStateChanged: true,
	}, h.store.Updates()[0].Locale)
	records := h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{"locale_content"}, records[0].ChangedFields)
	require.Equal(t, editor.memberID, records[0].MemberID)
	require.Equal(t, []string{"document_layout"}, records[1].ChangedFields)
}

func TestExecuteAIDocumentMutationCreatesATargetLocale(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	editor := h.member(newPrincipal())
	compiler := &metadataCompiler{patch: page.AIDocumentMetadataPatch{EnsureLocale: true, SetTitle: true, Title: text("번역")}}

	result, err := h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "ko", Mode: page.AIDocumentExecutionApply, Compile: compiler.compile,
	})

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotNil(t, result.TargetRevision)
	locale, exists := h.store.StoredLocale(stored.ID, "ko")
	require.True(t, exists)
	require.Equal(t, "번역", *locale.Title)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, h.audit.Records()[0].ItemOperation)
	require.Equal(t, []pagetest.OGRequest{{PageID: stored.ID, Locale: "ko", Reason: "page_ai_document_saved"}}, h.store.OGRequests())
}

func TestExecuteAIDocumentMutationAnswersCompilerAndRevisionFailures(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	editor := h.member(newPrincipal())
	compileFailed := errors.New("operation is not supported")
	_, err := h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionApply,
		Compile: (&metadataCompiler{err: compileFailed}).compile,
	})
	require.ErrorIs(t, err, compileFailed)

	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionApply,
		Compile: func(state page.AIDocumentState) (page.AIDocumentMutation, error) {
			mutation, err := (&metadataCompiler{patch: page.AIDocumentMetadataPatch{SetTitle: true, Title: text("T")}}).compile(state)
			stale := uuid.New()
			mutation.ExpectedRevision, mutation.Batch.ExpectedRevision = stale, stale
			return mutation, err
		},
	})
	var conflict *aidocument.RevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, aidocument.ConflictDocumentRevision, conflict.Kind)
	require.Equal(t, h.revision(t, stored.ID).String(), conflict.CurrentDocumentRevision)

	_, err = h.service.ExecuteAIDocumentMutation(editor.context(t), page.ExecuteAIDocumentMutationCommand{
		PageID: stored.ID, Locale: "en", Mode: page.AIDocumentExecutionApply,
		Compile: (&metadataCompiler{patch: page.AIDocumentMetadataPatch{SetDocumentLayout: true}}).compile,
	})
	requireCode(t, err, connect.CodeInvalidArgument, "document_layout: invalid contentHeight \"\"")
	require.Empty(t, h.audit.Records())
}
