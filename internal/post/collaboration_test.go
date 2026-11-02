package post_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/post"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// revision is the content revision of a stored Post.
func (h *harness) revision(t *testing.T, postID string) uuid.UUID {
	t.Helper()
	stored, ok := h.store.Post(postID)
	require.True(t, ok)
	document, ok := h.store.Document(uuid.MustParse(*stored.ContentDocumentID))
	require.True(t, ok)
	return document.Revision
}

func TestUpdatePostDocumentMetadataReplacesTaxonomyUnderTheDocumentRevision(t *testing.T) {
	h := newHarness(t)
	contributor := h.member(newPrincipal(), "Contributor")
	value := h.seedPost("Tagged", contributor.memberID)
	h.store.PutTaxonomy(post.Categories, "category-b", "category-a")
	h.store.PutTaxonomy(post.Tags, "tag-a")
	revision := h.revision(t, value.ID)
	command := post.UpdatePostDocumentMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: revision.String(), ContributorMemberIDs: []string{contributor.memberID},
		CategoryIDs: &[]string{"category-b", " category-a ", "category-b"},
	}

	saved, err := h.service.UpdatePostDocumentMetadata(t.Context(), command)

	require.NoError(t, err)
	require.True(t, saved.Changed)
	require.NotEqual(t, revision.String(), saved.DocumentRevision)
	require.Equal(t, []string{"category-a", "category-b"}, h.store.Links(post.Categories, value.ID))

	unchanged, err := h.service.UpdatePostDocumentMetadata(t.Context(), post.UpdatePostDocumentMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: saved.DocumentRevision, ContributorMemberIDs: []string{contributor.memberID},
		CategoryIDs: &[]string{"category-a", "category-b"},
	})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, saved.DocumentRevision, unchanged.DocumentRevision, "an unchanged taxonomy keeps the revision")

	_, err = h.service.UpdatePostDocumentMetadata(t.Context(), command)
	requireCode(t, err, connect.CodeFailedPrecondition, "Post document changed since it was loaded; reload before saving", "a stale revision is rejected")
	_, err = h.service.UpdatePostDocumentMetadata(t.Context(), post.UpdatePostDocumentMetadataCommand{
		PostID: value.ID, Locale: "ko", ExpectedRevision: saved.DocumentRevision, ContributorMemberIDs: []string{contributor.memberID},
		TagIDs: &[]string{"tag-a"},
	})
	require.Error(t, err, "a target locale cannot change the shared taxonomy")
	require.Empty(t, h.store.Links(post.Tags, value.ID))
}

func TestCollaborationSavesNeedContributorsWhoMayEdit(t *testing.T) {
	contributor := newPrincipal()
	h := newHarness(t, withChecker(&authztest.Checker{}))
	h.member(contributor, "Contributor")
	value := h.seedPost("Guarded", contributor.memberID)
	h.store.PutTaxonomy(post.Tags, "tag-a")

	_, err := h.service.UpdatePostDocumentMetadata(t.Context(), post.UpdatePostDocumentMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: h.revision(t, value.ID).String(),
		ContributorMemberIDs: []string{contributor.memberID}, TagIDs: &[]string{"tag-a"},
	})

	require.Error(t, err)
	require.Empty(t, h.store.Links(post.Tags, value.ID))
}

func TestCreatePostVersionCheckpointRecordsASourceVersion(t *testing.T) {
	h := newHarness(t)
	contributor := h.member(newPrincipal(), "Contributor")
	value := h.seedPost("Versioned", contributor.memberID)
	revision := h.revision(t, value.ID).String()
	command := post.CreatePostVersionCheckpointCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: revision, ContributorMemberIDs: []string{contributor.memberID},
	}

	ctx := collabContext(t)
	checkpoint, err := h.service.CreatePostVersionCheckpoint(ctx, command)

	require.NoError(t, err)
	require.True(t, checkpoint.Created)
	versions := h.store.Versions(value.ID)
	require.Len(t, versions, 1)
	require.Equal(t, *checkpoint.VersionID, versions[0].ID)
	require.EqualValues(t, 1, versions[0].Version)
	require.Equal(t, []string{contributor.memberID}, versions[0].ContributorMemberIDs)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditPostUpdated, records[0].Action)
	require.Equal(t, versions[0].ID, records[0].VersionID)

	target, err := h.service.CreatePostVersionCheckpoint(ctx, post.CreatePostVersionCheckpointCommand{
		PostID: value.ID, Locale: "ko", ExpectedRevision: revision, ContributorMemberIDs: []string{contributor.memberID},
	})
	require.NoError(t, err)
	require.False(t, target.Created, "a target locale room checkpoints no version")
	_, err = h.service.CreatePostVersionCheckpoint(ctx, post.CreatePostVersionCheckpointCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: uuid.NewString(), ContributorMemberIDs: []string{contributor.memberID},
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "")
	require.Len(t, h.store.Versions(value.ID), 1)
	require.Len(t, h.audit.Records(), 1)
}

func TestUpdatePostLocaleMetadataSavesTheSourceTitle(t *testing.T) {
	h := newHarness(t)
	contributor := h.member(newPrincipal(), "Contributor")
	value := h.seedPost("Before", contributor.memberID)
	revision := h.revision(t, value.ID)
	command := post.UpdatePostLocaleMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: revision.String(), ContributorMemberIDs: []string{contributor.memberID},
		TitleChange: &post.StringChange{Set: text("After")},
	}

	saved, err := h.service.UpdatePostLocaleMetadata(t.Context(), command)

	require.NoError(t, err)
	require.True(t, saved.Changed)
	require.True(t, saved.SourceChanged)
	require.Equal(t, []string{"en"}, saved.ChangedLocales)
	require.NotEqual(t, revision.String(), saved.DocumentRevision)
	locale, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, "After", *locale.Title)
	updates := h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, post.ContentUpdate{
		PostID: value.ID, Fields: []string{"title"}, DocumentRevision: saved.DocumentRevision,
		Contributors: []string{contributor.memberID}, Source: post.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}, updates[0])
	require.Len(t, h.audit.Records(), 1)
	require.Equal(t, contributor.memberID, h.audit.Records()[0].MemberID)

	_, err = h.service.UpdatePostLocaleMetadata(t.Context(), command)
	requireCode(t, err, connect.CodeFailedPrecondition, "Post document changed since it was loaded; reload before saving", "a stale revision is rejected")
	_, err = h.service.UpdatePostLocaleMetadata(t.Context(), post.UpdatePostLocaleMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: saved.DocumentRevision, ContributorMemberIDs: []string{contributor.memberID},
	})
	requireCode(t, err, connect.CodeInvalidArgument, "at least one typed locale metadata change is required")
}

func TestLoadPostBlockDocumentNeedsAnEditingSession(t *testing.T) {
	reader := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(reader, "Reader")
	value := h.seedPost("Room", "")
	sessionID := uuid.NewString()
	h.store.PutSession(sessionID, reader.user())

	_, err := h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: value.ID, Locale: "en"})
	require.Error(t, err, "a room load names its session")
	_, err = h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: value.ID, Locale: "en", SessionID: text(uuid.NewString())})
	require.Error(t, err, "an unknown session has no principal")
	_, err = h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: value.ID, Locale: "en", SessionID: &sessionID})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this post")
	checker.For(reader.identityID).Allow(policyv1.Post.View(value.ID))
	_, err = h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: value.ID, Locale: "en", SessionID: &sessionID})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this post", "viewing is not enough to open the room")

	checker.For(reader.identityID).Allow(policyv1.Post.Edit(value.ID))
	room, err := h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: value.ID, Locale: "en", SessionID: &sessionID})
	require.NoError(t, err)
	require.Equal(t, h.revision(t, value.ID).String(), room.DocumentRevision)
}

func TestPostDocumentMetadataAuditsTheOriginAndOnlyChangedTaxonomies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	contributor := h.member(newPrincipal(), "Contributor")
	value := h.seedPost("Audited taxonomy", contributor.memberID)
	h.store.PutTaxonomy(post.Categories, "category-a")
	h.store.PutTaxonomy(post.Tags, "tag-a", "tag-b")
	ctx := collabContext(t)
	command := post.UpdatePostDocumentMetadataCommand{
		PostID: value.ID, Locale: "en", ExpectedRevision: h.revision(t, value.ID).String(),
		ContributorMemberIDs: []string{contributor.memberID},
		CategoryIDs:          &[]string{"category-a"}, TagIDs: &[]string{"tag-a"},
	}
	saved, err := h.service.UpdatePostDocumentMetadata(ctx, command)
	require.NoError(t, err)
	require.True(t, saved.Changed)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditPostUpdated, records[0].Action)
	require.Equal(t, sharedtelemetry.ActorKindMember, records[0].Kind)
	require.Equal(t, contributor.memberID, records[0].MemberID)
	require.Equal(t, value.ID, records[0].TargetID)
	require.Equal(t, []string{"categories", "tags"}, records[0].ChangedFields)
	updates := h.store.Updates()
	require.Len(t, updates, 1)
	require.Equal(t, post.ContentUpdate{
		PostID: value.ID, Fields: []string{"categoryIds", "tagIds"}, DocumentRevision: saved.DocumentRevision,
		Contributors: []string{contributor.memberID}, Source: post.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}, updates[0])

	command.ExpectedRevision = saved.DocumentRevision
	unchanged, err := h.service.UpdatePostDocumentMetadata(ctx, command)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, saved.DocumentRevision, unchanged.DocumentRevision)
	require.Len(t, h.audit.Records(), 1)
	require.Len(t, h.store.Updates(), 1)

	command.TagIDs = &[]string{"tag-b"}
	saved, err = h.service.UpdatePostDocumentMetadata(ctx, command)
	require.NoError(t, err)
	require.True(t, saved.Changed)
	records = h.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{"tags"}, records[1].ChangedFields)
	updates = h.store.Updates()
	require.Len(t, updates, 2)
	require.Equal(t, []string{"tagIds"}, updates[1].Fields)
	require.Equal(t, saved.DocumentRevision, updates[1].DocumentRevision)

	auditFailure := errors.New("audit unavailable")
	h.audit.Err = auditFailure
	command.ExpectedRevision = saved.DocumentRevision
	command.CategoryIDs = &[]string{}
	_, err = h.service.UpdatePostDocumentMetadata(ctx, command)
	require.ErrorIs(t, err, auditFailure)
	require.Equal(t, saved.DocumentRevision, h.revision(t, value.ID).String())
	require.Equal(t, []string{"category-a"}, h.store.Links(post.Categories, value.ID))
	require.Equal(t, []string{"tag-b"}, h.store.Links(post.Tags, value.ID))
	require.Len(t, h.audit.Records(), 2)
	require.Len(t, h.store.Updates(), 2)
}

func TestPostDocumentMetadataRequiresOneOriginMember(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	contributor := h.member(newPrincipal(), "Contributor")
	value := h.seedPost("Origin required", contributor.memberID)
	revision := h.revision(t, value.ID).String()
	for _, members := range [][]string{nil, {contributor.memberID, newPrincipal().memberID}, {"not-a-member-uuid"}} {
		_, err := h.service.UpdatePostDocumentMetadata(collabContext(t), post.UpdatePostDocumentMetadataCommand{
			PostID: value.ID, Locale: "en", ExpectedRevision: revision, ContributorMemberIDs: members, CategoryIDs: &[]string{},
		})
		requireCode(t, err, connect.CodeInvalidArgument, "")
		require.Equal(t, revision, h.revision(t, value.ID).String())
		require.Empty(t, h.audit.Records())
	}
}
