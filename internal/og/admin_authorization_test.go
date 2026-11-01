package og

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingAdminAuthorizer struct {
	requireAuthenticatedErr error
	requireAdminErr         error
	entityType              string
	entityID                string
	requireEdit             bool
	authorizeErr            error
}

func (a *recordingAdminAuthorizer) RequireAuthenticated(context.Context) error {
	return a.requireAuthenticatedErr
}

func (a *recordingAdminAuthorizer) RequireAdmin(context.Context) error { return a.requireAdminErr }

func (a *recordingAdminAuthorizer) AuthorizeEntity(_ context.Context, entityType, entityID string, requireEdit bool) error {
	a.entityType, a.entityID, a.requireEdit = entityType, entityID, requireEdit
	return a.authorizeErr
}

func TestAuthorizeOgEntityTargetDelegatesDomainPolicy(t *testing.T) {
	authorizer := &recordingAdminAuthorizer{}
	service := &Admin{authorizer: authorizer}
	entityID := "11111111-1111-4111-8111-111111111111"

	require.NoError(t, service.authorizeEntityTarget(context.Background(), "series", entityID, true))
	require.Equal(t, "series", authorizer.entityType)
	require.Equal(t, entityID, authorizer.entityID)
	require.True(t, authorizer.requireEdit)
}

func TestNormalizeOgAuthorizationEntityIDUsesCanonicalStaticIdentity(t *testing.T) {
	require.Equal(t, SiteEntityID, normalizeAuthorizationEntityID(
		"site", "",
	))
	require.Equal(t, PrivacyRouteEntityID, normalizeAuthorizationEntityID(
		"privacy", "ignored",
	))
	require.Equal(t, TermsRouteEntityID, normalizeAuthorizationEntityID(
		"terms", "ignored",
	))
}
