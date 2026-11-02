package post_test

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/post"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// checkpoint stores a version of a Post through the collaboration
// checkpoint and returns its ID.
func (h *harness) checkpoint(t *testing.T, postID string, contributor principal) string {
	t.Helper()
	created, err := h.service.CreatePostVersionCheckpoint(collabContext(t), post.CreatePostVersionCheckpointCommand{
		PostID: postID, Locale: "en", ExpectedRevision: h.revision(t, postID).String(), ContributorMemberIDs: []string{contributor.memberID},
	})
	require.NoError(t, err)
	require.True(t, created.Created)
	return *created.VersionID
}

func TestListPostVersionsPagesNewestFirst(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Versioned", editor.memberID)
	first := h.checkpoint(t, value.ID, editor)
	h.store.PutLocale(value.ID, post.LocaleMetadata{Locale: "en", Title: text("Renamed")})
	second := h.checkpoint(t, value.ID, editor)

	page, err := h.service.ListPostVersions(editor.context(t), post.ListPostVersionsQuery{PostID: value.ID, Page: post.Page{Limit: 1}})

	require.NoError(t, err)
	require.EqualValues(t, 2, page.Pagination.Total)
	require.Len(t, page.Versions, 1)
	require.Equal(t, second, page.Versions[0].ID)
	require.EqualValues(t, 2, page.Versions[0].Version)
	require.Equal(t, "Renamed", page.Versions[0].Title)
	page, err = h.service.ListPostVersions(editor.context(t), post.ListPostVersionsQuery{PostID: value.ID, Page: post.Page{Limit: 1, Offset: 1}})
	require.NoError(t, err)
	require.Equal(t, first, page.Versions[0].ID)
}

func TestRestorePostVersionBringsBackTheSourceMetadata(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Original", editor.memberID)
	versionID := h.checkpoint(t, value.ID, editor)
	h.store.PutLocale(value.ID, post.LocaleMetadata{Locale: "en", Title: text("Drifted")})
	before := h.revision(t, value.ID)

	restored, err := h.service.RestorePostVersion(editor.context(t), post.RestorePostVersionCommand{PostID: value.ID, VersionID: versionID})

	require.NoError(t, err)
	require.Equal(t, "Original", restored.Title)
	locale, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, "Original", *locale.Title)
	require.NotEqual(t, before, h.revision(t, value.ID))
	require.NotEmpty(t, h.store.Updates(), "a restore wakes translation up")
	require.NotEmpty(t, h.audit.Records())
}

func TestRestorePostVersionRejectionsChangeNothing(t *testing.T) {
	editor, outsider := newPrincipal(), newPrincipal()
	cases := []struct {
		name    string
		target  func(value, other post.Post) string
		version func(t *testing.T, h *harness, valueID, versionID string) string
		actor   principal
		code    connect.Code
		message string
	}{
		{name: "an unknown version", target: func(value, _ post.Post) string { return value.ID },
			version: func(*testing.T, *harness, string, string) string { return "00000000-0000-4000-8000-000000000001" },
			actor:   editor, code: connect.CodeNotFound, message: "post_version not found: 00000000-0000-4000-8000-000000000001"},
		{name: "a version of another Post", target: func(_, other post.Post) string { return other.ID },
			version: func(_ *testing.T, _ *harness, _, versionID string) string { return versionID },
			actor:   editor, code: connect.CodeNotFound},
		{name: "an invalid snapshot", target: func(value, _ post.Post) string { return value.ID },
			version: func(_ *testing.T, h *harness, valueID, _ string) string {
				return h.store.PutVersion(post.StoredVersion{PostID: valueID, Version: 2, ContentSnapshot: json.RawMessage(`{"document":"corrupt"}`)}).ID
			},
			actor: editor, code: connect.CodeFailedPrecondition, message: "Post Version typed snapshot is invalid"},
		{name: "no manage permission", target: func(value, _ post.Post) string { return value.ID },
			version: func(_ *testing.T, _ *harness, _, versionID string) string { return versionID },
			actor:   outsider, code: connect.CodePermissionDenied, message: "no permission to manage this post"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			checker := authztest.AllowAll()
			h := newHarness(t, withChecker(checker))
			h.member(editor, "Editor")
			h.member(outsider, "Outsider")
			value, other := h.seedPost("Original", editor.memberID), h.seedPost("Other", editor.memberID)
			checker.For(outsider.identityID).Deny(policyv1.Post.Manage(value.ID))
			versionID := h.checkpoint(t, value.ID, editor)
			revision := h.revision(t, value.ID)
			audits := len(h.audit.Records())
			requested := test.version(t, h, value.ID, versionID)

			_, err := h.service.RestorePostVersion(test.actor.context(t), post.RestorePostVersionCommand{PostID: test.target(value, other), VersionID: requested})

			if test.message == "" {
				test.message = "post_version not found: " + requested
			}
			requireCode(t, err, test.code, test.message)
			require.Equal(t, revision, h.revision(t, value.ID))
			require.Empty(t, h.store.Updates())
			require.Len(t, h.audit.Records(), audits)
		})
	}
}
