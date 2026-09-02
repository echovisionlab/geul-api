package account

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestAccountCredentialMutationFromSnapshots(t *testing.T) {
	oidc := func(provider, subject string) auth.Credential {
		return auth.Credential{
			Type:        "oidc",
			Identifiers: []string{provider + ":" + subject},
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": provider, "subject": subject,
			}}},
		}
	}
	passkeys := func(ids ...string) auth.Credential {
		entries := make(structured.Values, 0, len(ids))
		for _, id := range ids {
			entries = append(entries, structured.Fields{"id": id})
		}
		return auth.Credential{Type: "passkey", Config: structured.Fields{"credentials": entries}}
	}
	tests := []struct {
		name       string
		kind       AccountCredentialKind
		before     map[string]auth.Credential
		after      map[string]auth.Credential
		wantEvent  email.EventKey
		wantIDs    []string
		wantChange bool
		wantErr    error
	}{
		{name: "social added", kind: AccountCredentialOIDC, after: map[string]auth.Credential{"oidc": oidc("google", "subject")}, wantEvent: email.EventSocialLoginAdded, wantChange: true},
		{name: "social removed", kind: AccountCredentialOIDC, before: map[string]auth.Credential{"oidc": oidc("github", "subject")}, wantEvent: email.EventSocialLoginRemoved, wantChange: true},
		{name: "passkeys added", kind: AccountCredentialPasskey, before: map[string]auth.Credential{"passkey": passkeys("one")}, after: map[string]auth.Credential{"passkey": passkeys("one", "two", "three")}, wantEvent: email.EventPasskeyAdded, wantIDs: []string{"three", "two"}, wantChange: true},
		{name: "passkey removed", kind: AccountCredentialPasskey, before: map[string]auth.Credential{"passkey": passkeys("one", "two")}, after: map[string]auth.Credential{"passkey": passkeys("two")}, wantEvent: email.EventPasskeyRemoved, wantIDs: []string{"one"}, wantChange: true},
		{name: "no-op", kind: AccountCredentialOIDC, before: map[string]auth.Credential{"oidc": oidc("google", "subject")}, after: map[string]auth.Credential{"oidc": oidc("google", "subject")}},
		{name: "provider replacement is not one transition", kind: AccountCredentialOIDC, before: map[string]auth.Credential{"oidc": oidc("google", "one")}, after: map[string]auth.Credential{"oidc": oidc("github", "two")}, wantErr: ErrAccountCredentialMutationShape},
		{name: "passkey replacement is not one transition", kind: AccountCredentialPasskey, before: map[string]auth.Credential{"passkey": passkeys("one")}, after: map[string]auth.Credential{"passkey": passkeys("two")}, wantErr: ErrAccountCredentialMutationShape},
		{name: "oidc hook cannot change passkeys", kind: AccountCredentialOIDC, before: map[string]auth.Credential{"oidc": oidc("google", "one")}, after: map[string]auth.Credential{"oidc": oidc("google", "one"), "passkey": passkeys("one")}, wantErr: ErrAccountCredentialMutationShape},
		{name: "passkey hook cannot change oidc", kind: AccountCredentialPasskey, before: map[string]auth.Credential{"passkey": passkeys("one")}, after: map[string]auth.Credential{"passkey": passkeys("one"), "oidc": oidc("google", "one")}, wantErr: ErrAccountCredentialMutationShape},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutation, changed, err := accountCredentialMutationFromSnapshots(AccountCredentialHookInput{
				Kind: tt.kind, PreviousCredentials: tt.before, Credentials: tt.after,
			})
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantChange, changed)
			require.Equal(t, tt.wantEvent, mutation.event)
			require.Equal(t, tt.wantIDs, mutation.passkeyIDs)
		})
	}
}
