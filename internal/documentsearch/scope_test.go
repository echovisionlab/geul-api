package documentsearch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
)

type fixedRoles struct {
	inactive bool
	admin    bool
	author   bool
	err      error
}

func (roles fixedRoles) IsActive(context.Context) (bool, error) { return !roles.inactive, roles.err }
func (roles fixedRoles) IsAdmin(context.Context) (bool, error)  { return roles.admin, nil }
func (roles fixedRoles) IsAuthor(context.Context) (bool, error) { return roles.author, nil }

type ownedIDs struct {
	ids   []string
	err   error
	calls int
}

func (owned *ownedIDs) OwnedDocumentIDs(context.Context) ([]string, error) {
	owned.calls++
	return owned.ids, owned.err
}

func newScopes(t *testing.T, roles documentsearch.CallerRoles, posts *ownedIDs) *documentsearch.OwnershipScopes {
	t.Helper()
	scopes, err := documentsearch.NewOwnershipScopes(roles, map[core.Domain]documentsearch.OwnedDocuments{core.DomainPost: posts})
	require.NoError(t, err)
	return scopes
}

// A Site Admin's lookup would list every document, for a filter that removes none.
func TestAnAdminScopeCoversEveryDocumentWithoutLookingAnythingUp(t *testing.T) {
	posts := &ownedIDs{}
	scope, err := newScopes(t, fixedRoles{admin: true, author: true}, posts).Resolve(t.Context(), []core.Domain{core.DomainPost})

	require.NoError(t, err)
	require.True(t, scope.All)
	require.True(t, scope.Allows("anything"))
	require.Zero(t, posts.calls)
}

func TestAnAuthorScopeCoversOnlyOwnedDocumentsOfTheSearchedTypes(t *testing.T) {
	posts := &ownedIDs{ids: []string{"own-post"}}
	scopes := newScopes(t, fixedRoles{author: true}, posts)

	scope, err := scopes.Resolve(t.Context(), []core.Domain{core.DomainPost, core.DomainPage})
	require.NoError(t, err)
	require.False(t, scope.All)
	require.True(t, scope.Allows("own-post"))
	require.False(t, scope.Allows("other-post"))

	// A type the caller does not search costs no lookup, and one they cannot own is empty.
	posts.calls = 0
	scope, err = scopes.Resolve(t.Context(), []core.Domain{core.DomainPage})
	require.NoError(t, err)
	require.False(t, scope.Allows("own-post"))
	require.Zero(t, posts.calls)
}

// A collaborator without the Author role may view a live Post. Their scope stays
// empty, because a scope does not know when that Post is archived.
func TestACallerWithoutTheAuthorRoleFindsNothing(t *testing.T) {
	posts := &ownedIDs{ids: []string{"collaborated-post"}}
	scope, err := newScopes(t, fixedRoles{}, posts).Resolve(t.Context(), []core.Domain{core.DomainPost})

	require.NoError(t, err)
	require.False(t, scope.Allows("collaborated-post"))
	require.Zero(t, posts.calls)
}

// Managing an Artist or a Label needs no role, so a caller without one finds what
// they manage. Only a Post depends on the Author role.
func TestACallerWithoutARoleFindsWhatTheyManageExceptPosts(t *testing.T) {
	posts, labels := &ownedIDs{ids: []string{"collaborated-post"}}, &ownedIDs{ids: []string{"managed-label"}}
	scopes, err := documentsearch.NewOwnershipScopes(fixedRoles{}, map[core.Domain]documentsearch.OwnedDocuments{core.DomainPost: posts, core.DomainLabel: labels})
	require.NoError(t, err)

	scope, err := scopes.Resolve(t.Context(), []core.Domain{core.DomainPost, core.DomainLabel})

	require.NoError(t, err)
	require.True(t, scope.Allows("managed-label"))
	require.False(t, scope.Allows("collaborated-post"))
	require.Zero(t, posts.calls)
}

func TestAnInactiveCallerFindsNothingWhateverTheirRole(t *testing.T) {
	posts := &ownedIDs{ids: []string{"own-post"}}
	scope, err := newScopes(t, fixedRoles{inactive: true, admin: true, author: true}, posts).Resolve(t.Context(), []core.Domain{core.DomainPost})

	require.NoError(t, err)
	require.False(t, scope.All)
	require.False(t, scope.Allows("own-post"))
}

func TestAScopeThatCannotBeResolvedFailsTheSearchRatherThanWidening(t *testing.T) {
	failure := errors.New("authorizer unavailable")
	_, err := newScopes(t, fixedRoles{err: failure}, &ownedIDs{}).Resolve(t.Context(), []core.Domain{core.DomainPost})
	require.ErrorIs(t, err, failure)

	_, err = newScopes(t, fixedRoles{author: true}, &ownedIDs{err: failure}).Resolve(t.Context(), []core.Domain{core.DomainPost})
	require.ErrorIs(t, err, failure)
}
