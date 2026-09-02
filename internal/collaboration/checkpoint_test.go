package collaboration

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fixedContributorResolver map[string]auth.AccountIdentitySubject

func (r fixedContributorResolver) ResolveActiveSubjects(
	context.Context,
	*gorm.DB,
	[]string,
) (map[string]auth.AccountIdentitySubject, error) {
	return r, nil
}

func TestCheckpointFenceRechecksEveryActiveContributor(t *testing.T) {
	t.Parallel()
	memberID := uuid.NewString()
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}
	authorizer := &recordingAuthorizer{allowed: true}
	registry := NewRegistry(Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		Authorizer:   authorizer,
	})
	fence := NewCheckpointFence(registry, fixedContributorResolver{memberID: subject})

	require.NoError(t, fence.RequireCurrentContributors(
		t.Context(),
		&gorm.DB{},
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		uuid.NewString(),
		[]string{memberID},
	))
	require.Equal(t, subject, authorizer.subject)
}

func TestCheckpointFenceUsesRequestedPermissionWithoutEditFallback(t *testing.T) {
	t.Parallel()
	memberID := uuid.NewString()
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}
	authorizer := &recordingAuthorizer{allowed: true}
	fence := NewCheckpointFence(NewRegistry(Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
		Authorizer:   authorizer,
	}), fixedContributorResolver{memberID: subject})

	require.NoError(t, fence.RequireCurrentContributorsForPermission(
		t.Context(), &gorm.DB{},
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_MENU,
		uuid.NewString(), []string{memberID},
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE,
	))
	require.Equal(t, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_MANAGE, authorizer.permission)
}

func TestCheckpointFenceRejectsInactiveOrUnsortedContributors(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		Authorizer:   &recordingAuthorizer{allowed: true},
	})
	fence := NewCheckpointFence(registry, fixedContributorResolver{})
	first, second := uuid.NewString(), uuid.NewString()
	if first < second {
		first, second = second, first
	}

	err := fence.RequireCurrentContributors(
		t.Context(), &gorm.DB{},
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		uuid.NewString(), []string{first, second},
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	err = fence.RequireCurrentContributors(
		t.Context(), &gorm.DB{},
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		uuid.NewString(), []string{uuid.NewString()},
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
