package account

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDeletedUserCompletionEmailUsesDurableMemberReference(t *testing.T) {
	memberID := uuid.NewString()
	identityID := uuid.NewString()
	email := "member@example.test"
	name := "Member"
	locale := "ko"
	command := DeleteIdentityCommand{
		MemberID:           memberID,
		IdentityID:         identityID,
		NotificationEmail:  &email,
		NotificationName:   &name,
		NotificationLocale: &locale,
	}

	job := deletedUserCompletionEmail(command)

	require.NotNil(t, job)
	require.Equal(t, memberID, *job.ReferenceID)
	require.Equal(t, email, job.Recipient)
	require.Equal(t, name, job.TemplateData["name"])
}

func TestDeletedUserCompletionEmailRequiresSnapshot(t *testing.T) {
	require.Nil(t, deletedUserCompletionEmail(DeleteIdentityCommand{
		MemberID:   uuid.NewString(),
		IdentityID: uuid.NewString(),
	}))
}

func TestBuildUserDeleteAvatarCommandPreservesStableCleanupIdentity(t *testing.T) {
	memberID := uuid.NewString()
	assetID := uuid.NewString()
	command := buildUserDeleteAvatarCommand(DeleteIdentityCommand{
		MemberID:      memberID,
		AvatarAssetID: &assetID,
	})

	require.Equal(t, memberID, command.MemberID)
	require.Equal(t, assetID, *command.AvatarAssetID)
}

func TestBuildUserDeleteAvatarCommandAllowsMissingAvatarAsset(t *testing.T) {
	command := buildUserDeleteAvatarCommand(DeleteIdentityCommand{MemberID: uuid.NewString()})

	require.Nil(t, command.AvatarAssetID)
}
