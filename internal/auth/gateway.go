package auth

import (
	"context"
	"errors"
	"net/http"

	"gorm.io/gorm"
)

type gatewayIdentityKey struct{}

// GatewayIdentity is populated only after the canonical session has resolved
// through the active Identity and its bilateral Member link.
type GatewayIdentity struct {
	IdentityID IdentityID
	MemberID   MemberID
	SessionID  SessionID
}

// RequireGatewaySession projects the Oathkeeper-overwritten X-Session-Id
// principal into request context for raw HTTP handlers. The session header is
// the only public actor authority; legacy gateway assertions are not read.
func RequireGatewaySession(db *gorm.DB, next http.Handler) http.Handler {
	if next == nil {
		panic("gateway handler is required")
	}
	if db == nil {
		panic("member link database is required")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := ResolveAuthenticatedPrincipalBySessionID(
			r.Context(), db,
			r.Header.Get("X-Session-Id"),
		)
		if err != nil {
			if errors.Is(err, ErrSessionPrincipalInvalid) {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			} else {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			}
			return
		}
		if principal.Banned {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if !principal.Onboarded {
			http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
			return
		}
		ctx := context.WithValue(r.Context(), gatewayIdentityKey{}, GatewayIdentity{
			IdentityID: principal.IdentityID,
			MemberID:   principal.MemberID,
			SessionID:  principal.SessionID,
		})
		ctx = WithUser(ctx, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GatewayIdentityFromContext(ctx context.Context) (GatewayIdentity, bool) {
	identity, ok := ctx.Value(gatewayIdentityKey{}).(GatewayIdentity)
	return identity, ok && identity.IdentityID != "" && identity.MemberID != "" && identity.SessionID != ""
}
