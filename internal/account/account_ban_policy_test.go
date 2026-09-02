//go:build integration

package account

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

type accountBanTargetIdentityGetter struct {
	identity *auth.Identity
	err      error
}

func (getter accountBanTargetIdentityGetter) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return getter.identity, getter.err
}

func (getter accountBanTargetIdentityGetter) GetIdentityWithIncludeCredential(
	context.Context,
	string,
	string,
) (*auth.Identity, error) {
	return getter.identity, getter.err
}

func TestEnsureAccountBanTargetIsNonAdminUsesIdentityRoleAuthority(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	service := &AccountService{spicedb: stack.SpiceDBClient}
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	t.Run("ordinary identity is bannable", func(t *testing.T) {
		_, err := service.accountBanTargetIdentity(t.Context(), accountBanTargetIdentityGetter{
			identity: &auth.Identity{ID: author.IdentityID},
		}, author.IdentityID)

		require.NoError(t, err)
	})

	t.Run("admin identity is never bannable", func(t *testing.T) {
		_, err := service.accountBanTargetIdentity(t.Context(), accountBanTargetIdentityGetter{
			identity: &auth.Identity{ID: admin.IdentityID},
		}, admin.IdentityID)

		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		require.ErrorContains(t, err, "admin accounts cannot be banned")
	})

	t.Run("identity lookup failure is fail closed", func(t *testing.T) {
		_, err := service.accountBanTargetIdentity(t.Context(), accountBanTargetIdentityGetter{
			err: errors.New("identity unavailable"),
		}, author.IdentityID)

		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})

	t.Run("missing identity is fail closed", func(t *testing.T) {
		_, err := service.accountBanTargetIdentity(t.Context(), accountBanTargetIdentityGetter{}, author.IdentityID)

		require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})
}
