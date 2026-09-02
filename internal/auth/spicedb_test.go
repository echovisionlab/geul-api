package auth

import (
	"testing"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestNewSpiceDBClientRequiresConnectionCredentials(t *testing.T) {
	client, err := NewSpiceDBClient("", "token", true)
	require.Nil(t, client)
	require.EqualError(t, err, "SpiceDB endpoint is required")

	client, err = NewSpiceDBClient("spicedb:50051", "  ", true)
	require.Nil(t, client)
	require.EqualError(t, err, "SpiceDB API token is required")
}

func TestZedTokenKeepsOpaqueValueAndRejectsInvalidProviderValues(t *testing.T) {
	token, err := parseZedToken("opaque-revision")
	require.NoError(t, err)
	require.Equal(t, "opaque-revision", token.String())
	consistency, err := atLeastAsFreshSpiceDB(token)
	require.NoError(t, err)
	require.Equal(t, "opaque-revision", consistency.GetAtLeastAsFresh().GetToken())

	for _, raw := range []string{"", " opaque-revision", "opaque-revision\n"} {
		_, err := parseZedToken(raw)
		require.Error(t, err, raw)
	}
}

func TestGeneratedRelationshipMutationTranslatesThroughClosedCatalog(t *testing.T) {
	actor, err := policyv1.NewAccountIdentityActor("00000000-0000-4000-8000-000000000001")
	require.NoError(t, err)
	typed, err := policyv1.Post.TouchAuthor("post-1", actor)
	require.NoError(t, err)
	mutation, err := translateRelationshipMutation(typed)
	require.NoError(t, err)
	require.Equal(t, "post", mutation.update.GetRelationship().GetResource().GetObjectType())
	require.Equal(t, "author", mutation.update.GetRelationship().GetRelation())
	require.Equal(t, "account_identity", mutation.update.GetRelationship().GetSubject().GetObject().GetObjectType())
}

func TestGeneratedResourcePolicyAndParentMutationsAreTyped(t *testing.T) {
	policyMutation, err := policyv1.Post.TouchPolicy("post-1")
	require.NoError(t, err)
	require.Equal(t, "platform", policyMutation.SubjectType())

	parentMutation, err := policyv1.Artist.TouchParent("artist-1", "artist-2")
	require.NoError(t, err)
	require.Equal(t, "artist", parentMutation.Resource().Type())
	require.Equal(t, "artist", parentMutation.SubjectType())
}
