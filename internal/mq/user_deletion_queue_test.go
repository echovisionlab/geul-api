package mq

import (
	"testing"

	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestUserDeletionCommandIdentityIsStable(t *testing.T) {
	memberID := "14600000-0000-1000-8000-000000000001"
	for _, command := range []userDeletionCommand{
		&managev1.UserDeleteIdentityCommand{MemberId: memberID},
		&managev1.UserDeleteAvatarCommand{MemberId: memberID},
	} {
		got, err := userDeletionCommandMemberID(command)
		require.NoError(t, err)
		require.Equal(t, memberID, got)
	}
	_, err := userDeletionCommandMemberID(&managev1.UserDeleteIdentityCommand{})
	require.Error(t, err)
}
