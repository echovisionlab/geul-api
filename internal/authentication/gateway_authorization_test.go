package authentication

import (
	"testing"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestGatewayAuthorizationCanUsesSharedAuthorizationRole(t *testing.T) {
	tests := []struct {
		name string
		role policyv1.AuthorizationRole
		want func() (policyv1.Can, error)
		ok   bool
	}{
		{name: "author", role: policyv1.AuthorizationRole_AUTHOR, want: policyv1.Platform.IsAuthor, ok: true},
		{name: "admin", role: policyv1.AuthorizationRole_ADMIN, want: policyv1.Platform.IsAdmin, ok: true},
		{name: "user", role: policyv1.AuthorizationRole_USER},
		{name: "anon", role: policyv1.AuthorizationRole_ANON},
		{name: "unspecified", role: policyv1.AuthorizationRole_UNSPECIFIED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			can, ok, err := gatewayAuthorizationCan(tt.role)
			if err != nil {
				t.Fatalf("gatewayAuthorizationCan(%s): %v", tt.role, err)
			}
			if ok != tt.ok {
				t.Fatalf("gatewayAuthorizationCan(%s) ok = %t, want %t", tt.role, ok, tt.ok)
			}
			if !tt.ok {
				if can.Valid() {
					t.Fatalf("gatewayAuthorizationCan(%s) returned a valid descriptor for an unsupported role", tt.role)
				}
				return
			}
			want, err := tt.want()
			if err != nil {
				t.Fatalf("build expected Can: %v", err)
			}
			if can.EngineKey() != want.EngineKey() || can.Action().Name() != want.Action().Name() {
				t.Fatalf("gatewayAuthorizationCan(%s) = (%q, %q), want (%q, %q)", tt.role, can.Action().Name(), can.EngineKey(), want.Action().Name(), want.EngineKey())
			}
		})
	}
}
