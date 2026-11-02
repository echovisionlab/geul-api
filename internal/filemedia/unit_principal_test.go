package filemedia

import (
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
)

// unitUser is an onboarded principal with fresh IDs, for tests whose
// authorization an authztest.Checker answers instead of SpiceDB and Kratos.
type unitUser struct {
	IdentityID string
	MemberID   string
	SessionID  string
}

func newUnitUser() *unitUser {
	return &unitUser{IdentityID: uuid.NewString(), MemberID: uuid.NewString(), SessionID: uuid.NewString()}
}

func (user *unitUser) AuthUserInfo() *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(user.IdentityID),
		MemberID:      auth.MemberID(user.MemberID),
		SessionID:     auth.SessionID(user.SessionID),
		Authenticated: true,
		Onboarded:     true,
	}
}
