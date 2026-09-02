package mcp

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

type principalContextKey struct{}

// ErrInvalidPrincipal marks an incomplete gateway assertion. It contains no
// request header or credential value.
var ErrInvalidPrincipal = errors.New("mcp: invalid asserted principal")

// WithPrincipal binds one already authenticated gateway principal to a request
// context. It never accepts or stores a bearer credential.
func WithPrincipal(ctx context.Context, principal Principal) (context.Context, error) {
	if !assertedPrincipalValid(principal) {
		return ctx, ErrInvalidPrincipal
	}
	return context.WithValue(ctx, principalContextKey{}, principal), nil
}

// PrincipalFromContext returns a defensive copy of the trusted principal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok || !assertedPrincipalValid(principal) {
		return Principal{}, false
	}
	return principal, true
}

func assertedPrincipalValid(principal Principal) bool {
	if _, err := uuidutil.ParseCanonical(principal.IdentityID, "identity ID"); err != nil {
		return false
	}
	if _, err := uuidutil.ParseCanonical(principal.MemberID, "member ID"); err != nil {
		return false
	}
	return validDelegationAttribution(principal.DelegationID, 2048, 0) &&
		validDelegationAttribution(principal.DelegationName, 400, 100) &&
		principal.DelegationMethod == DelegationMethodMCPOAuth
}

func validDelegationAttribution(value string, maxBytes, maxRunes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
