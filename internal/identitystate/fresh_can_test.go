package identitystate_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestRequireFreshCanMarksOnlyTheInactivePrincipalDenial(t *testing.T) {
	principal := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
	ctx := auth.WithUser(t.Context(), principal)
	can, err := policyv1.Menu.Edit(uuid.NewString())
	require.NoError(t, err)

	inactive := identitystate.RequireFreshCan(ctx, nil, (&identitystatetest.Fence{}).Deactivate(principal.IdentityID.String()), authztest.AllowAll(), can)
	require.ErrorIs(t, inactive, identitystate.ErrInactivePrincipal)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(inactive))
	require.Equal(t, errs.NoPermission(can.Action().Name(), can.Resource().Type()).Message(), errs.Wrap(inactive).Message())

	denied := identitystate.RequireFreshCan(ctx, nil, &identitystatetest.Fence{}, (&authztest.Checker{}).Deny(can, nil), can)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(denied))
	require.False(t, errors.Is(denied, identitystate.ErrInactivePrincipal))
}
