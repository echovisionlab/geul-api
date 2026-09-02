//go:build integration

package account

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func requireAccountIdentitySubject(t *testing.T, identityID string) auth.AccountIdentitySubject {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	return subject
}

func requireGlobalRolePermission(
	t *testing.T,
	ctx context.Context,
	spicedb *auth.SpiceDBClient,
	identityID string,
	canFor func() (policyv1.Can, error),
	want bool,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := canFor()
	require.NoError(t, err)
	allowed, err := spicedb.CheckActorCan(ctx, actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed, can.Action().Name())
}
