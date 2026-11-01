package authztest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var _ mutation.Authorizer = (*authztest.Checker)(nil)

func principalContext(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func TestCheckerAnswersByTheMostSpecificRule(t *testing.T) {
	postID, owner, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	edit, err := policyv1.Post.Edit(postID)
	require.NoError(t, err)
	view, err := policyv1.Post.View(postID)
	require.NoError(t, err)

	checker := (&authztest.Checker{}).Allow(policyv1.Post.View(postID)).For(owner).Allow(policyv1.Post.Edit(postID))

	require.NoError(t, authz.RequireCan(principalContext(owner), checker, edit))
	require.Error(t, authz.RequireCan(principalContext(other), checker, edit))
	require.NoError(t, authz.RequireCan(principalContext(other), checker, view))
	require.Equal(t, []authztest.Check{
		authztest.Of(owner, edit, nil), authztest.Of(other, edit, nil), authztest.Of(other, view, nil),
	}, checker.Checks())

	checker.Deny(policyv1.Post.Edit(postID))
	require.Error(t, authz.RequireCan(principalContext(owner), checker, edit), "a rule for everyone replaces the identity rule")

	all := authztest.AllowAll().Deny(policyv1.Post.Edit(postID))
	require.NoError(t, authz.RequireCan(principalContext(owner), all, view))
	require.Error(t, authz.RequireCan(principalContext(owner), all, edit))

	down := errors.New("SpiceDB unavailable")
	all.Fail(down)
	_, err = all.CheckActorCan(context.Background(), mustActor(t, owner), view)
	require.ErrorIs(t, err, down)
}

func TestCheckerClientServesTheSameRules(t *testing.T) {
	postID, owner := uuid.NewString(), uuid.NewString()
	edit, err := policyv1.Post.Edit(postID)
	require.NoError(t, err)
	checker := (&authztest.Checker{}).For(owner).Allow(policyv1.Post.Edit(postID))
	client := checker.Client(t)

	require.NoError(t, authz.RequireCan(principalContext(owner), client, edit))
	require.Error(t, authz.RequireCan(principalContext(uuid.NewString()), client, edit))
	allowed, err := client.CheckActorCan(t.Context(), mustActor(t, owner), edit)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Len(t, checker.Checks(), 3)
	require.Equal(t, authztest.Of(owner, edit, nil), checker.Checks()[0])

	checker.Fail(errors.New("SpiceDB unavailable"))
	_, err = client.CheckActorCan(t.Context(), mustActor(t, owner), edit)
	require.Error(t, err)
}

func TestCheckerRecordsRelationshipWrites(t *testing.T) {
	touch, err := policyv1.SiteSetting.TouchPolicy("1")
	require.NoError(t, err)
	remove, err := policyv1.SiteSetting.DeletePolicy("1")
	require.NoError(t, err)
	checker := &authztest.Checker{}
	batch := []policyv1.RelationshipMutation{touch}
	inverse := []policyv1.RelationshipMutation{remove}

	_, err = checker.ApplyRelationshipsExpecting(t.Context(), batch, inverse)
	require.NoError(t, err)
	_, err = checker.CompensateRelationshipsExpecting(t.Context(), inverse, batch)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{batch}, checker.Applied())
	require.Equal(t, [][]policyv1.RelationshipMutation{inverse}, checker.Compensated())

	refused := errors.New("precondition failed")
	checker.FailWrites(refused)
	_, err = checker.ApplyRelationshipsExpecting(t.Context(), batch, inverse)
	require.ErrorIs(t, err, refused)
	require.Len(t, checker.Applied(), 1)
}

func mustActor(t *testing.T, identityID string) policyv1.Actor {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	return actor
}
