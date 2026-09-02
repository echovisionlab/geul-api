//go:build integration

package programevent

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/google/uuid"
)

func programEventIntegrationAdminCtx(id string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(integrationMemberID(id)),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}

func ptrBool(value bool) *bool {
	return &value
}
