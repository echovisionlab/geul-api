package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPrincipalContextRejectsIncompleteOrNoncanonicalAssertions(t *testing.T) {
	valid := validPrincipal()
	for _, principal := range []Principal{
		{},
		{MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: " member ", DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName},
		{IdentityID: valid.IdentityID, MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: "unknown"},
		{IdentityID: strings.ToUpper(valid.IdentityID), MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: valid.IdentityID, MemberID: "00000000-0000-0000-0000-000000000000", DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
		{IdentityID: "aaaaaaaa-aaaa-0aaa-8aaa-aaaaaaaaaaaa", MemberID: valid.MemberID, DelegationID: valid.DelegationID, DelegationName: valid.DelegationName, DelegationMethod: valid.DelegationMethod},
	} {
		ctx, err := WithPrincipal(context.Background(), principal)
		if !errors.Is(err, ErrInvalidPrincipal) {
			t.Fatalf("WithPrincipal(%+v) error = %v", principal, err)
		}
		if _, ok := PrincipalFromContext(ctx); ok {
			t.Fatalf("invalid principal was stored: %+v", principal)
		}
	}
}

func TestPrincipalContextReturnsVerifiedPrincipal(t *testing.T) {
	principal := validPrincipal()
	ctx, err := WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := PrincipalFromContext(ctx)
	if !ok || stored != principal {
		t.Fatalf("stored principal = %+v/%v", stored, ok)
	}
}
