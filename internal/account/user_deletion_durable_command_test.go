package account

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestDeletedUserCompletionEmailUsesDurableMemberReference(t *testing.T) {
	memberID := uuid.NewString()
	identityID := uuid.NewString()
	email := "member@example.test"
	name := "Member"
	locale := "ko"
	command := &managev1.UserDeleteIdentityCommand{
		MemberId:           memberID,
		IdentityId:         identityID,
		NotificationEmail:  &email,
		NotificationName:   &name,
		NotificationLocale: &locale,
	}

	job := deletedUserCompletionEmail(command)

	require.NotNil(t, job)
	require.Equal(t, memberID, job.GetReferenceId())
	require.Equal(t, email, job.GetRecipient())
	require.Equal(t, name, job.GetTemplateData()["name"])
}

func TestDeletedUserCompletionEmailRequiresSnapshot(t *testing.T) {
	require.Nil(t, deletedUserCompletionEmail(&managev1.UserDeleteIdentityCommand{
		MemberId:   uuid.NewString(),
		IdentityId: uuid.NewString(),
	}))
}

func TestBuildUserDeleteAvatarCommandPreservesStableCleanupIdentity(t *testing.T) {
	memberID := uuid.NewString()
	assetID := uuid.NewString()
	command := buildUserDeleteAvatarCommand(&managev1.UserDeleteIdentityCommand{
		MemberId:      memberID,
		AvatarAssetId: &assetID,
	})

	require.Equal(t, memberID, command.GetMemberId())
	require.Equal(t, assetID, command.GetAvatarAssetId())
}

func TestBuildUserDeleteAvatarCommandAllowsMissingAvatarAsset(t *testing.T) {
	command := buildUserDeleteAvatarCommand(&managev1.UserDeleteIdentityCommand{MemberId: uuid.NewString()})

	require.Empty(t, command.GetAvatarAssetId())
}
