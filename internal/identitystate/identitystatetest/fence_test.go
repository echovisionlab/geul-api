package identitystatetest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var _ identitystate.Fence = (*identitystatetest.Fence)(nil)

func TestFenceReportsDeactivatedAndIncompletePrincipalsInactive(t *testing.T) {
	active := &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString())}
	deactivated := &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString())}
	fence := (&identitystatetest.Fence{}).Deactivate(deactivated.IdentityID.String())
	db := &gorm.DB{}

	for principal, want := range map[*auth.UserInfo]bool{
		active: true, deactivated: false, {IdentityID: active.IdentityID}: false, {MemberID: active.MemberID}: false,
	} {
		got, err := fence.LockActivePrincipal(t.Context(), db, principal)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	require.ElementsMatch(t, []string{active.IdentityID.String(), deactivated.IdentityID.String()}, fence.Locked())
}

func TestFenceRunsMutationWorkAfterLockingTheIdentity(t *testing.T) {
	fence := &identitystatetest.Fence{}
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	var ran bool
	require.NoError(t, fence.WithMutation(t.Context(), db, "identity", func(context.Context, *gorm.DB) error {
		ran = true
		require.Equal(t, []string{"identity"}, fence.Locked())
		return nil
	}))
	require.True(t, ran)
	require.Error(t, fence.Lock(db, " "))

	down := errors.New("fence unavailable")
	failing := &identitystatetest.Fence{Err: down}
	require.ErrorIs(t, failing.WithMutation(t.Context(), db, "identity", func(context.Context, *gorm.DB) error {
		t.Fatal("work must not run without the fence")
		return nil
	}), down)
}
