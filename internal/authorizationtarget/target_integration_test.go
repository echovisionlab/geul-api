//go:build integration

package authorizationtarget

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestForMemberRequiresAnActiveOnboardedLinkedTargetIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())

	target, err := ForMember(stack.DB.WithContext(t.Context()), user.MemberID)
	require.NoError(t, err)
	require.NotEqual(t, user.IdentityID, user.MemberID)
	require.Equal(t, user.MemberID, target.MemberID)
	require.Equal(t, user.IdentityID, target.IdentityID)

	require.NoError(t, stack.DB.Exec(`UPDATE member SET onboarded=FALSE WHERE id=?::uuid`, user.MemberID).Error)
	_, err = ForMember(stack.DB.WithContext(t.Context()), user.MemberID)
	require.ErrorIs(t, err, ErrIneligible)

	require.NoError(t, stack.DB.Exec(`UPDATE member SET onboarded=TRUE WHERE id=?::uuid`, user.MemberID).Error)
	require.NoError(t, stack.DB.Exec(
		`UPDATE kratos.identities SET state='inactive' WHERE id=?::uuid`, user.IdentityID,
	).Error)
	_, err = ForMember(stack.DB.WithContext(t.Context()), user.MemberID)
	require.ErrorIs(t, err, ErrIneligible)
}

func TestEligibleMemberIDsAndLockReferencesIgnoreTemporaryIdentityStateIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())

	require.NoError(t, stack.DB.Exec(
		`UPDATE kratos.identities SET state='inactive', metadata_admin='{"banned":true}'::jsonb WHERE id=?::uuid`,
		user.IdentityID,
	).Error)
	eligible, err := EligibleMemberIDs(t.Context(), stack.DB, []string{user.MemberID})
	require.NoError(t, err)
	require.Equal(t, []string{user.MemberID}, eligible)
	for _, field := range []string{
		"work.member_id",
		"track.member_id",
		"release.member_id",
		"program_event.member_id",
	} {
		t.Run(field+" preserves temporary account state", func(t *testing.T) {
			require.NoError(t, stack.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
				return LockReferences(t.Context(), tx, []Reference{{MemberID: user.MemberID, Field: field}})
			}))
		})
	}

	require.NoError(t, stack.DB.Exec(`UPDATE member SET onboarded=FALSE WHERE id=?::uuid`, user.MemberID).Error)
	eligible, err = EligibleMemberIDs(t.Context(), stack.DB, []string{user.MemberID})
	require.NoError(t, err)
	require.Empty(t, eligible)
	for _, field := range []string{
		"work.member_id",
		"track.member_id",
		"release.member_id",
		"program_event.member_id",
	} {
		t.Run(field+" rejects unonboarded member", func(t *testing.T) {
			err = stack.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
				return LockReferences(t.Context(), tx, []Reference{{MemberID: user.MemberID, Field: field}})
			})
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestValidateActivePairRejectsIdentityAsMemberIDIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())

	err := ValidateActivePair(t.Context(), stack.DB, user.IdentityID, user.IdentityID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be distinct")
}
