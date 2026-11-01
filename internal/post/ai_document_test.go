package post_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/post"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAIDocumentMutationAuthorizesTheLockedLifecycleOnce(t *testing.T) {
	for name, test := range map[string]struct {
		status post.Status
		action func(string) (policyv1.Can, error)
	}{
		"a draft needs edit":                   {status: post.StatusDraft, action: policyv1.Post.Edit},
		"an archived Post needs archived edit": {status: post.StatusArchived, action: policyv1.Post.EditArchived},
	} {
		t.Run(name, func(t *testing.T) {
			editor := newPrincipal()
			checker := authztest.AllowAll()
			h := newHarness(t, withChecker(checker))
			h.member(editor, "Editor")
			value := h.seedPost("AI", "", func(value *post.Post) { value.Status = test.status })
			stop := errors.New("stop after the authorized compiler")
			calls := 0

			_, err := h.service.ExecuteAIDocumentMutation(editor.context(t), value.ID, "en", post.AIDocumentExecutionValidate,
				func(state post.AIDocumentState) (post.AIDocumentMutation, error) {
					calls++
					require.Equal(t, value.ID, state.PostID)
					return post.AIDocumentMutation{}, stop
				})

			require.ErrorIs(t, err, stop)
			require.Equal(t, 1, calls)
			want, err := test.action(value.ID)
			require.NoError(t, err)
			require.Equal(t, []authztest.Check{authztest.Of(editor.identityID, want, nil)}, checker.Checks(),
				"one logical mutation makes one exact authorization decision")
		})
	}
}

func TestAIDocumentMutationDeniesBeforeCompiling(t *testing.T) {
	editor := newPrincipal()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	h.member(editor, "Editor")
	value := h.seedPost("AI", "", func(value *post.Post) { value.Status = post.StatusArchived })
	calls := 0

	_, err := h.service.ExecuteAIDocumentMutation(editor.context(t), value.ID, "en", post.AIDocumentExecutionApply,
		func(post.AIDocumentState) (post.AIDocumentMutation, error) {
			calls++
			return post.AIDocumentMutation{}, nil
		})

	requireCode(t, err, connect.CodeNotFound, "")
	require.Zero(t, calls, "the denial precedes projection and compilation")
	want, err := policyv1.Post.EditArchived(value.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(editor.identityID, want, nil)}, h.checker.Checks())
}

func TestAIDocumentEditIsAuditedOnlyWhenApplied(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Before", editor.memberID)
	ctx := editor.context(t)
	retitle := func(mode post.AIDocumentExecutionMode, title string) {
		t.Helper()
		_, err := h.service.ExecuteAIDocumentMutation(ctx, value.ID, "en", mode, func(state post.AIDocumentState) (post.AIDocumentMutation, error) {
			revision := uuid.MustParse(state.DocumentRevision)
			contributor := uuid.MustParse(state.ViewerMemberID)
			return post.AIDocumentMutation{
				PostID: state.PostID, Locale: state.RequestedLocale,
				ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
				ExpectedRevision: revision, ContributorMemberID: contributor,
				Batch: contentblock.Batch{
					DocumentID: uuid.MustParse(state.ContentDocumentID), ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor},
				},
				Metadata: post.AIDocumentMetadataPatch{SetTitle: true, Title: &title},
			}, nil
		})
		require.NoError(t, err)
	}

	retitle(post.AIDocumentExecutionValidate, "Validated")
	require.Empty(t, h.audit.Records(), "validation writes no record")
	locale, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, "Before", *locale.Title, "validation changes nothing")

	retitle(post.AIDocumentExecutionApply, "Applied")
	locale, _ = h.store.Locale(value.ID, "en")
	require.Equal(t, "Applied", *locale.Title)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, editor.memberID, records[0].MemberID)
	require.Contains(t, records[0].ChangedFields, "locale_content")

	retitle(post.AIDocumentExecutionApply, "Applied")
	require.Len(t, h.audit.Records(), 1, "an edit that changes nothing leaves no record")
}
