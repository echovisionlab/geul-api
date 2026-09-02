package auth

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/telemetry"
)

type contextKey struct{}

// IdentityID is the durable provider-neutral account_identity UUID. MemberID
// is a separate product/profile projection; converting one to the other is
// never an authentication or authorization fallback.
type IdentityID string
type MemberID string
type SessionID string

func (id IdentityID) String() string { return string(id) }
func (id MemberID) String() string   { return string(id) }
func (id SessionID) String() string  { return string(id) }

// UserInfo is the bounded authenticated principal. Product profile fields are
// read from MemberService and are deliberately absent from auth context.
type UserInfo struct {
	IdentityID      IdentityID
	MemberID        MemberID
	SessionID       SessionID
	AuthenticatedAt time.Time
	Authenticated   bool
	Banned          bool
	Onboarded       bool
}

const securityMutationFreshness = 3 * time.Hour

// IsFreshForSecurityMutation applies the bounded Kratos session
// authenticated_at freshness requirement used by account security mutations.
func IsFreshForSecurityMutation(user *UserInfo, now time.Time) bool {
	if user == nil || user.AuthenticatedAt.IsZero() || now.Before(user.AuthenticatedAt) {
		return false
	}
	return now.Sub(user.AuthenticatedAt) <= securityMutationFreshness
}

// WithUser returns a new context with the given UserInfo.
func WithUser(ctx context.Context, user *UserInfo) context.Context {
	ctx = context.WithValue(ctx, contextKey{}, user)
	if user == nil || !user.Authenticated || !user.Onboarded || user.MemberID == "" {
		return telemetry.WithActor(ctx, telemetry.AnonymousActor{})
	}
	return telemetry.WithActor(ctx, telemetry.MemberActor{
		SessionID:  user.SessionID.String(),
		IdentityID: user.IdentityID.String(),
		MemberID:   user.MemberID.String(),
	})
}

// GetUser extracts the UserInfo from the context. Returns nil if not present.
func GetUser(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(contextKey{}).(*UserInfo)
	return u
}
