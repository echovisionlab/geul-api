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

type recordingAuthorizer struct {
	allowed    bool
	resourceID string
	permission intrav1.CollaborationPermission
	subject    auth.AccountIdentitySubject
}

func (a *recordingAuthorizer) Authorize(
	_ context.Context,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	a.resourceID = resourceID
	a.permission = permission
	a.subject = subject
	return a.allowed, nil
}

func (a *recordingAuthorizer) AuthorizeInTx(
	ctx context.Context,
	_ *gorm.DB,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	return a.Authorize(ctx, resourceID, permission, subject)
}

func TestRegistryRequireCurrentEditUsesOnlyCanonicalContextPrincipal(t *testing.T) {
	t.Parallel()
	identityID := uuid.NewString()
	resourceID := uuid.NewString()
	authorizer := &recordingAuthorizer{allowed: true}
	registry := NewRegistry(Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT,
		Authorizer:   authorizer,
	})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})

	require.NoError(t, registry.RequireCurrentEdit(
		ctx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT,
		resourceID,
	))
	require.Equal(t, resourceID, authorizer.resourceID)
	require.Equal(t, intrav1.CollaborationPermission_COLLABORATION_PERMISSION_EDIT, authorizer.permission)
	require.Equal(t, identityID, authorizer.subject.ID.String())

	authorizer.allowed = false
	err := registry.RequireCurrentEdit(
		ctx,
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PROGRAM_EVENT,
		resourceID,
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestRegistryRejectsUnregisteredResourceType(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	_, err := registry.Authorize(
		t.Context(),
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PAGE,
		uuid.NewString(),
		intrav1.CollaborationPermission_COLLABORATION_PERMISSION_VIEW,
		auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())},
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
