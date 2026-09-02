package auth

import (
	"fmt"
	"strings"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
)

// ZedToken is an opaque SpiceDB revision returned by a successful relationship
// mutation. It sets a lower bound for a server-internal dependent read; it does
// not replace an ordinary fully-consistent final authorization check.
type ZedToken struct {
	value string
}

// parseZedToken validates an opaque provider-returned revision. Public request
// transport does not select authorization consistency.
func parseZedToken(raw string) (ZedToken, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n") {
		return ZedToken{}, fmt.Errorf("ZedToken is required")
	}
	return ZedToken{value: raw}, nil
}

func (token ZedToken) String() string { return token.value }

func zedTokenFromResponse(token *v1.ZedToken, operation string) (ZedToken, error) {
	if token == nil {
		return ZedToken{}, fmt.Errorf("SpiceDB returned an empty %s ZedToken", operation)
	}
	parsed, err := parseZedToken(token.GetToken())
	if err != nil {
		return ZedToken{}, fmt.Errorf("SpiceDB returned an invalid %s ZedToken: %w", operation, err)
	}
	return parsed, nil
}

func fullyConsistentSpiceDB() *v1.Consistency {
	return &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}}
}

func atLeastAsFreshSpiceDB(token ZedToken) (*v1.Consistency, error) {
	parsed, err := parseZedToken(token.String())
	if err != nil {
		return nil, err
	}
	return &v1.Consistency{Requirement: &v1.Consistency_AtLeastAsFresh{
		AtLeastAsFresh: &v1.ZedToken{Token: parsed.String()},
	}}, nil
}

func atExactSnapshotSpiceDB(token ZedToken) (*v1.Consistency, error) {
	parsed, err := parseZedToken(token.String())
	if err != nil {
		return nil, err
	}
	return &v1.Consistency{Requirement: &v1.Consistency_AtExactSnapshot{
		AtExactSnapshot: &v1.ZedToken{Token: parsed.String()},
	}}, nil
}
