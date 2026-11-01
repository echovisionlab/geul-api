//go:build integration

package programevent_test

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
)

func programEventIntegrationAdminCtx(id string) context.Context {
	return auth.WithUser(requestBackground(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(testutil.PostIntegrationMemberID(id)),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	})
}

func ptrBool(value bool) *bool {
	return &value
}
