package legal_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// sourceTitleMutation sets the source title of the loaded state.
func sourceTitleMutation(title string) legal.AIDocumentMutationCompiler {
	return func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
		return legal.AIDocumentMutation{
			EntityType: state.EntityType, EntityID: state.EntityID, Locale: state.Locale,
			ExpectedRevision: state.Revision, SetTitle: true, Title: &title, ContributorMemberID: state.ViewerMemberID,
		}, nil
	}
}

// createTranslation creates the loaded target locale with title.
func createTranslation(title string) legal.AIDocumentMutationCompiler {
	return func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
		return legal.AIDocumentMutation{
			EntityType: state.EntityType, EntityID: state.EntityID, Locale: state.Locale,
			ExpectedRevision: state.Revision, SetTitle: true, Title: &title,
			Translation: legal.AITranslationCreate, ContributorMemberID: state.ViewerMemberID,
		}, nil
	}
}

func TestExecuteAIDocumentMutationValidatesWithoutCommitting(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	past := time.Now().Add(-time.Hour)
	active, revision := h.putVersion(t, legal.Terms, "active", func(v *legal.Version) { v.EffectiveFrom = &past })

	result, err := h.documents.ExecuteAIDocumentMutation(ctx, "terms", active.ID, "en", legal.AIDocumentExecutionValidate, sourceTitleMutation("Validated"))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, active.Title, h.stored(t, legal.Terms, active.ID).Title, "validation rolls the write back")
	require.Equal(t, revision, h.revision(t, legal.Terms, active.ID))
	require.Empty(t, h.store.Updates())

	result, err = h.documents.ExecuteAIDocumentMutation(ctx, "terms", active.ID, "en", legal.AIDocumentExecutionApply, sourceTitleMutation("Applied"))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, "Applied", h.stored(t, legal.Terms, active.ID).Title)
	require.Equal(t, h.revision(t, legal.Terms, active.ID), result.Revision)
	require.Len(t, h.store.Updates(), 1)
	require.Equal(t, legal.ContentUpdate{
		Kind: legal.Terms, PolicyID: active.ID, Locale: "en", DocumentRevision: result.Revision,
		ContributorMemberID: h.store.Updates()[0].ContributorMemberID,
	}, h.store.Updates()[0])
	require.Equal(t, "terms_ai_document_saved", h.store.OGRequests()[0].Reason)
	require.Empty(t, h.audit.Records(), "a source write records no locale content")

	_, err = h.documents.ExecuteAIDocumentMutation(ctx, "terms", active.ID, "en", legal.AIDocumentExecutionApply, sourceTitleMutation("Stale"))
	require.NoError(t, err, "the compiler reads the locked state, so it never sees a stale revision")
	_, err = h.documents.ExecuteAIDocumentMutation(ctx, "terms", active.ID, "en", legal.AIDocumentExecutionApply,
		func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
			mutation, err := sourceTitleMutation("x")(state)
			mutation.ExpectedRevision = uuid.NewString()
			return mutation, err
		})
	requireAnswer(t, err, connect.CodeInvalidArgument, "mutation: compiled Legal identity, locale, contributor and revision must match the locked state")
}

func TestExecuteAIDocumentMutationCreatesAndDeletesATranslation(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	ctx := caller.context(t)
	draft, revision := h.putVersion(t, legal.Privacy, "draft", nil)

	created, err := h.documents.ExecuteAIDocumentMutation(ctx, "privacy", draft.ID, "ko", legal.AIDocumentExecutionApply, createTranslation("개인정보"))
	require.NoError(t, err)
	require.True(t, created.Changed)
	require.Equal(t, revision, created.Revision)
	require.NotNil(t, created.TargetRevision)
	stored, ok := h.store.StoredTranslation(legal.Privacy, draft.ID, "ko")
	require.True(t, ok)
	require.Equal(t, ptr("개인정보"), stored.Title)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, caller.memberID, records[0].MemberID)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)

	loaded, err := h.documents.LoadAIDocument(ctx, "privacy", draft.ID, "ko")
	require.NoError(t, err)
	require.True(t, loaded.LocaleExists)
	require.Equal(t, created.TargetRevision, loaded.TargetRevision)

	deleted, err := h.documents.ExecuteAIDocumentMutation(ctx, "privacy", draft.ID, "ko", legal.AIDocumentExecutionApply,
		func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
			return legal.AIDocumentMutation{
				EntityType: state.EntityType, EntityID: state.EntityID, Locale: state.Locale,
				ExpectedRevision: state.Revision, ExpectedTargetRevision: state.TargetRevision,
				Translation: legal.AITranslationDelete, ContributorMemberID: state.ViewerMemberID,
			}, nil
		})
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	_, ok = h.store.StoredTranslation(legal.Privacy, draft.ID, "ko")
	require.False(t, ok)
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, h.audit.Records()[1].ItemOperation)
	require.True(t, h.store.Updates()[1].Deleted)

	_, err = h.documents.ExecuteAIDocumentMutation(ctx, "privacy", draft.ID, "ko", legal.AIDocumentExecutionApply,
		func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
			mutation, err := createTranslation("x")(state)
			expected := "tr1_stale"
			mutation.ExpectedTargetRevision = &expected
			return mutation, err
		})
	var conflict *legal.AIDocumentRevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, legal.AIDocumentTargetRevisionConflict, conflict.Kind)
}

func TestAIDocumentMasksWhatTheCallerMayNotEdit(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	ctx := caller.context(t)
	archived, _ := h.putVersion(t, legal.Terms, "archived", nil)
	compiled := false
	compiler := func(state legal.AIDocument) (legal.AIDocumentMutation, error) {
		compiled = true
		return sourceTitleMutation("x")(state)
	}

	h.checker.Deny(policyv1.TermsHistory.EditArchived(archived.ID))
	_, err := h.documents.ExecuteAIDocumentMutation(ctx, "terms", archived.ID, "en", legal.AIDocumentExecutionApply, compiler)
	requireAnswer(t, err, connect.CodeNotFound, "terms not found: "+archived.ID)
	require.False(t, compiled, "the compiler runs only after the decision")
	_, err = h.documents.LoadAIDocument(ctx, "terms", archived.ID, "en")
	require.NoError(t, err, "view_archived still reads it")

	h.fence.Deactivate(caller.identityID)
	_, err = h.documents.ExecuteAIDocumentMutation(ctx, "terms", archived.ID, "en", legal.AIDocumentExecutionApply, compiler)
	requireAnswer(t, err, connect.CodeNotFound, "terms not found: "+archived.ID)
	_, err = h.documents.LoadAIDocument(ctx, "terms", archived.ID, "en")
	requireAnswer(t, err, connect.CodeNotFound, "legal policy not found")
	_, err = h.documents.LoadAIDocument(ctx, "cookies", archived.ID, "en")
	requireAnswer(t, err, connect.CodeInvalidArgument, "entity_type: must be privacy or terms")
}

func TestTranslationInterchangeJoinsTheTranslationTransaction(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	ctx := caller.context(t)
	archived, _ := h.putVersion(t, legal.Terms, "archived", nil)
	audit := &audittest.Recorder{}
	joined := legal.Joined{Store: h.store, Audit: audit}

	state, err := h.documents.LoadTranslationInterchangeAIDocument(ctx, joined, "terms", archived.ID, "ko")
	require.NoError(t, err)
	require.False(t, state.LocaleExists)
	result, err := h.documents.ExecuteTranslationInterchangeMutation(ctx, joined, "terms", archived.ID, "ko", createTranslation("약관"))
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Len(t, audit.Records(), 1, "the record is appended through the joined transaction")
	require.Equal(t, caller.memberID, audit.Records()[0].MemberID)
	require.Empty(t, h.store.OGRequests(), "Translation plans the OG of an interchange")
	require.Len(t, h.store.Updates(), 1)
}

func TestTranslationFencesAuthorizeTheLockedLifecycle(t *testing.T) {
	h := newHarness(t)
	caller := newPrincipal()
	ctx := caller.context(t)
	archived, _ := h.putVersion(t, legal.Privacy, "archived", nil)
	draft, _ := h.putVersion(t, legal.Privacy, "draft", nil)
	lockActive := func(principal *auth.UserInfo) (bool, error) {
		return h.fence.LockActivePrincipal(ctx, nil, principal)
	}
	h.checker.Deny(policyv1.PrivacyHistory.EditArchived(archived.ID))

	require.NoError(t, legal.RequireTranslationInterchangeView(ctx, h.store, h.checker, lockActive, "privacy", archived.ID), "view_archived opens an export")
	err := legal.RequireEditableTranslationMutation(ctx, h.store, h.checker, lockActive, "privacy", archived.ID)
	requireAnswer(t, err, connect.CodePermissionDenied, "no permission to edit_archived this legal policy")
	require.NoError(t, legal.RequireLockedSourceLocaleEdit(ctx, h.store, h.checker, lockActive, "privacy", draft.ID))
	require.NoError(t, legal.RequireEditableTranslationMutation(ctx, h.store, h.checker, lockActive, "page", draft.ID), "another entity type is not Legal's")
	missing := uuid.NewString()
	err = legal.RequireLockedSourceLocaleEdit(ctx, h.store, h.checker, lockActive, "privacy", missing)
	requireAnswer(t, err, connect.CodeNotFound, "privacy not found: "+missing)

	h.checker.Deny(policyv1.PrivacyHistory.View(draft.ID))
	err = legal.RequireTranslationInterchangeView(ctx, h.store, h.checker, lockActive, "privacy", draft.ID)
	requireAnswer(t, err, connect.CodeNotFound, "privacy not found: "+draft.ID)
	h.fence.Deactivate(caller.identityID)
	err = legal.RequireLockedSourceLocaleEdit(ctx, h.store, h.checker, lockActive, "privacy", draft.ID)
	requireAnswer(t, err, connect.CodePermissionDenied, "no permission to edit this legal policy")
}

// typedTranslation is a whole-locale candidate of a version's source.
func typedTranslation(t *testing.T, h *harness, kind legal.PolicyKind, id, title, text string) (legal.TranslationJob, *translation.Candidate) {
	t.Helper()
	source, err := legal.LoadTypedTranslationSourceDocument(t.Context(), h.store, string(kind), id)
	require.NoError(t, err)
	overlay := proto.Clone(source.ContentBlockDocument.GetLocaleOverlay()).(*contentv1.RichTextLocaleOverlay)
	overlay.Locale = "ko"
	overlay.Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
		Text: &contentv1.RichTextStyledText{Text: text},
	}}}
	return legal.TranslationJob{
		EntityType: string(kind), EntityID: id, SourceLocale: source.SourceLocale, TargetLocale: "ko",
		RequestedByMemberID: uuid.NewString(),
	}, &translation.Candidate{
		Title: &title, ContentDocumentRevision: source.ContentDocumentRevision, ContentBlockLocaleOverlay: overlay,
	}
}

func TestApplyTypedTranslationCandidateWritesTheTargetLocaleInEveryLifecycle(t *testing.T) {
	for _, state := range []string{"draft", "scheduled", "active", "archived"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			version, revision := h.putVersion(t, legal.Terms, state, nil)
			audit := &audittest.Recorder{}
			joined := legal.Joined{Store: h.store, Audit: audit}
			job, candidate := typedTranslation(t, h, legal.Terms, version.ID, " 번역 제목 ", "번역 본문")

			require.NoError(t, legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate))
			stored, ok := h.store.StoredTranslation(legal.Terms, version.ID, "ko")
			require.True(t, ok)
			require.Equal(t, ptr("번역 제목"), stored.Title)
			require.Contains(t, stored.Projection.HTML, "번역 본문")
			require.Equal(t, revision, h.revision(t, legal.Terms, version.ID))
			require.Len(t, audit.Records(), 1)
			require.Equal(t, job.RequestedByMemberID, audit.Records()[0].MemberID)
			require.Equal(t, sharedtelemetry.AuditItemOperationCreated, audit.Records()[0].ItemOperation)

			_, replacement := typedTranslation(t, h, legal.Terms, version.ID, "다시", "다시 번역")
			require.NoError(t, legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, replacement))
			require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, audit.Records()[1].ItemOperation)
		})
	}
}

func TestApplyTypedTranslationCandidateRejectsWhatItCannotApply(t *testing.T) {
	h := newHarness(t)
	draft, _ := h.putVersion(t, legal.Privacy, "draft", nil)
	joined := legal.Joined{Store: h.store, Audit: &audittest.Recorder{}}
	job, candidate := typedTranslation(t, h, legal.Privacy, draft.ID, "제목", "본문")
	blank := " "

	for name, test := range map[string]struct {
		change func(*legal.TranslationJob, *translation.Candidate)
		code   connect.Code
		answer string
	}{
		"an unsupported entity":     {change: func(j *legal.TranslationJob, _ *translation.Candidate) { j.EntityType = "page" }, code: connect.CodeInvalidArgument, answer: "entity_type: legal typed translation requires terms or privacy"},
		"a non-canonical source":    {change: func(j *legal.TranslationJob, _ *translation.Candidate) { j.SourceLocale = "EN" }, code: connect.CodeInvalidArgument, answer: "locale: must be an exact canonical locale"},
		"a non-canonical target":    {change: func(j *legal.TranslationJob, _ *translation.Candidate) { j.TargetLocale = "ko-KR" }, code: connect.CodeInvalidArgument, answer: "locale: must be an exact canonical locale"},
		"an overlay of another one": {change: func(j *legal.TranslationJob, _ *translation.Candidate) { j.TargetLocale = "ja" }, code: connect.CodeInvalidArgument, answer: "document.locale_overlay.locale: must match the translation target locale"},
		"a blank title":             {change: func(_ *legal.TranslationJob, c *translation.Candidate) { c.Title = &blank }, code: connect.CodeFailedPrecondition, answer: "legal target locale title is required"},
		"a missing version": {change: func(j *legal.TranslationJob, _ *translation.Candidate) {
			j.EntityID = "e0b0a3b9-0000-4000-8000-000000000000"
		}, code: connect.CodeNotFound, answer: "privacy not found: e0b0a3b9-0000-4000-8000-000000000000"},
	} {
		t.Run(name, func(t *testing.T) {
			job, candidate := job, *candidate
			test.change(&job, &candidate)
			err := legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, &candidate)
			require.Equal(t, test.code, connect.CodeOf(err), err)
			require.Equal(t, test.answer, errs.Wrap(err).Message())
		})
	}
	err := legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, nil)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	stale := *candidate
	stale.ContentDocumentRevision = uuid.NewString()
	require.ErrorIs(t, legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, &stale), translation.ErrSourceNoLongerCurrent)
	_, exists := h.store.StoredTranslation(legal.Privacy, draft.ID, "ko")
	require.False(t, exists)

	h.store.Fail = map[string]error{"InsertTranslation": errors.New("insert failed")}
	require.Error(t, legal.ApplyTypedTranslationCandidate(t.Context(), joined, job, candidate))
}
