package post_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAddPostAuthorPromotesAMemberWithTheAuthorRole(t *testing.T) {
	h := newHarness(t)
	manager := h.member(newPrincipal(), "Manager")
	collaborator := h.member(newPrincipal(), "Collaborator")
	value := h.seedPost("Shared", manager.memberID)
	h.store.PutParticipant(value.ID, collaborator.memberID, post.RoleCollaborator, time.Now())

	added, err := h.service.AddPostAuthor(manager.context(t), post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID})

	require.NoError(t, err)
	require.Equal(t, post.RoleAuthor, added.Role)
	require.Equal(t, "Collaborator", added.Member.Nickname)
	_, isAuthor := h.store.Participant(value.ID, collaborator.memberID, post.RoleAuthor)
	require.True(t, isAuthor)
	_, isCollaborator := h.store.Participant(value.ID, collaborator.memberID, post.RoleCollaborator)
	require.False(t, isCollaborator, "an Author is no longer a collaborator")
	require.Len(t, h.checker.Applied(), 1)
	require.Len(t, h.audit.Records(), 1)

	again, err := h.service.AddPostAuthor(manager.context(t), post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID})
	require.NoError(t, err)
	require.Equal(t, added.CreatedAt, again.CreatedAt, "adding an Author again keeps the relation")
	require.Len(t, h.audit.Records(), 1, "an unchanged role is not audited")
}

func TestAddPostAuthorRejectsMembersWhoCannotAuthor(t *testing.T) {
	manager, writer, gone := newPrincipal(), newPrincipal(), newPrincipal()
	checker := authztest.AllowAll()
	checker.For(writer.identityID).Deny(policyv1.Platform.IsAuthor())
	h := newHarness(t, withChecker(checker))
	h.member(manager, "Manager")
	h.member(writer, "Writer")
	h.store.PutMember(gone.memberID, posttest.Member{IdentityID: gone.identityID, Nickname: "Gone"})
	value := h.seedPost("Guarded", manager.memberID)
	ctx := manager.context(t)

	_, err := h.service.AddPostAuthor(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: writer.memberID})
	requireCode(t, err, connect.CodeInvalidArgument, "member_id: post authors must have the site Author or Admin role")
	_, err = h.service.AddPostAuthor(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: gone.memberID})
	requireCode(t, err, connect.CodeNotFound, "", "an inactive Member has no authorization target")
	require.Empty(t, h.checker.Applied())
	require.Empty(t, h.audit.Records())
}

func TestRemovePostAuthorKeepsADurableAuthor(t *testing.T) {
	h := newHarness(t)
	first := h.member(newPrincipal(), "First")
	second := h.member(newPrincipal(), "Second")
	value := h.seedPost("Authored", first.memberID)
	ctx := first.context(t)

	err := h.service.RemovePostAuthor(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: first.memberID})
	requireCode(t, err, connect.CodeFailedPrecondition, "a post must retain at least one durable author")
	err = h.service.RemovePostAuthor(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: second.memberID})
	requireCode(t, err, connect.CodeNotFound, "post author relation not found")

	h.store.PutParticipant(value.ID, second.memberID, post.RoleAuthor, time.Now())
	require.NoError(t, h.service.RemovePostAuthor(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: first.memberID}))
	_, isAuthor := h.store.Participant(value.ID, first.memberID, post.RoleAuthor)
	require.False(t, isAuthor)
	require.Len(t, h.checker.Applied(), 1)
	require.Len(t, h.audit.Records(), 1)
}

func TestAddPostCollaboratorDemotesAnAuthorOnlyWhenAnotherRemains(t *testing.T) {
	h := newHarness(t)
	first := h.member(newPrincipal(), "First")
	second := h.member(newPrincipal(), "Second")
	value := h.seedPost("Demoted", first.memberID)
	ctx := first.context(t)

	_, err := h.service.AddPostCollaborator(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: first.memberID})
	requireCode(t, err, connect.CodeFailedPrecondition, "a post must retain at least one durable author")
	_, stillAuthor := h.store.Participant(value.ID, first.memberID, post.RoleAuthor)
	require.True(t, stillAuthor)

	h.store.PutParticipant(value.ID, second.memberID, post.RoleAuthor, time.Now())
	demoted, err := h.service.AddPostCollaborator(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: first.memberID})
	require.NoError(t, err)
	require.Equal(t, post.RoleCollaborator, demoted.Role)
	_, stillAuthor = h.store.Participant(value.ID, first.memberID, post.RoleAuthor)
	require.False(t, stillAuthor)
	_, isCollaborator := h.store.Participant(value.ID, first.memberID, post.RoleCollaborator)
	require.True(t, isCollaborator)
}

func TestRemovePostCollaboratorRemovesOnlyAnExistingRelation(t *testing.T) {
	h := newHarness(t)
	author := h.member(newPrincipal(), "Author")
	collaborator := h.member(newPrincipal(), "Collaborator")
	value := h.seedPost("Collaborated", author.memberID)
	ctx := author.context(t)

	err := h.service.RemovePostCollaborator(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID})
	requireCode(t, err, connect.CodeNotFound, "post collaborator relation not found")

	_, err = h.service.AddPostCollaborator(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID})
	require.NoError(t, err)
	require.NoError(t, h.service.RemovePostCollaborator(ctx, post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID}))
	_, isCollaborator := h.store.Participant(value.ID, collaborator.memberID, post.RoleCollaborator)
	require.False(t, isCollaborator)
	require.Len(t, h.checker.Applied(), 2)
	require.Len(t, h.audit.Records(), 2)
}

func TestParticipantRowsRollBackWhenTheirRelationshipsFail(t *testing.T) {
	h := newHarness(t, withChecker(authztest.AllowAll().FailWrites(errDown)))
	author := h.member(newPrincipal(), "Author")
	collaborator := h.member(newPrincipal(), "Collaborator")
	value := h.seedPost("Rolled back", author.memberID)

	_, err := h.service.AddPostCollaborator(author.context(t), post.PostParticipantCommand{PostID: value.ID, MemberID: collaborator.memberID})

	require.ErrorIs(t, err, errDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	_, isCollaborator := h.store.Participant(value.ID, collaborator.memberID, post.RoleCollaborator)
	require.False(t, isCollaborator)
}

func TestListPostParticipantsReportsEffectiveAuthority(t *testing.T) {
	reader, author, revoked := newPrincipal(), newPrincipal(), newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(reader, "Reader")
	h.member(author, "Author")
	h.member(revoked, "Revoked")
	value := h.seedPost("Listed", author.memberID)
	h.store.PutParticipant(value.ID, revoked.memberID, post.RoleCollaborator, time.Now())
	ctx := reader.context(t)

	_, err := h.service.ListPostParticipants(ctx, post.PostParticipantsQuery{PostID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "")

	checker.For(reader.identityID).Allow(policyv1.Post.View(value.ID))
	checker.For(author.identityID).Allow(policyv1.Post.Edit(value.ID))
	participants, err := h.service.ListPostParticipants(ctx, post.PostParticipantsQuery{PostID: value.ID})
	require.NoError(t, err)
	require.Len(t, participants, 2)
	require.Equal(t, "Author", participants[0].Member.Nickname)
	require.Equal(t, post.RoleAuthor, participants[0].Role)
	require.True(t, participants[0].HasEffectiveAuthority)
	require.Equal(t, post.RoleCollaborator, participants[1].Role)
	require.False(t, participants[1].HasEffectiveAuthority, "a collaborator who may no longer edit has no effective authority")
}
