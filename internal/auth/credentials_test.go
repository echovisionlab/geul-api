package auth

import (
	"reflect"
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestPasskeyCredentialIDsCanonicalizeConcreteCredentials(t *testing.T) {
	credential := Credential{Type: "passkey", Config: structured.Fields{
		"credentials": structured.Values{
			structured.Fields{"id": " key-b "},
			structured.Fields{"id": "key-a"},
			structured.Fields{"id": "key-a"},
			structured.Fields{"display_name": "missing-id"},
		},
	}}

	if got, want := PasskeyCredentialIDs(credential), []string{"key-a", "key-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PasskeyCredentialIDs() = %#v, want %#v", got, want)
	}
	if got := UsablePasskeyCredentialCount(credential); got != 2 {
		t.Fatalf("UsablePasskeyCredentialCount() = %d, want 2", got)
	}
}

func TestCredentialInventoryDeduplicatesOIDCConfigAndIdentifiers(t *testing.T) {
	credential := Credential{
		Type:        "oidc",
		Identifiers: []string{"google:subject-1", "github:subject-2"},
		Config: structured.Fields{
			"providers": structured.Values{
				structured.Fields{"provider": "google", "subject": "subject-1"},
			},
		},
	}

	if got := len(NewCredentialInventory(map[string]Credential{"oidc": credential}).OIDCProviders()); got != 2 {
		t.Fatalf("expected 2 usable providers, got %d", got)
	}
}

func TestCredentialInventoryUsesCanonicalKratosOIDCFields(t *testing.T) {
	inventory := NewCredentialInventory(map[string]Credential{
		"oidc": {
			Type:        "oidc",
			Identifiers: []string{"google:subject-1", "github:subject-2"},
			Config: structured.Fields{
				"providers": structured.Values{
					structured.Fields{
						"provider":             "Google",
						"subject":              "subject-1",
						"initial_access_token": "access-token",
						"initial_id_token":     "header.eyJlbWFpbCI6InVzZXJAZXhhbXBsZS50ZXN0IiwiZW1haWxfdmVyaWZpZWQiOnRydWV9.signature",
					},
					structured.Fields{
						"provider_name": "unsupported",
						"sub":           "unsupported-subject",
					},
				},
			},
		},
	})

	providers := inventory.OIDCProviders()
	if len(providers) != 2 {
		t.Fatalf("providers = %#v, want canonical config plus identifier-only provider", providers)
	}
	if got := providers[0]; got.Provider != "google" || got.Subject != "subject-1" ||
		got.InitialAccessToken != "access-token" || len(got.ClaimSets) != 1 ||
		got.ClaimSets[0].Email != "user@example.test" || !got.ClaimSets[0].EmailVerified {
		t.Fatalf("canonical provider = %#v", got)
	}
	if got := providers[1]; got.Provider != "github" || got.Subject != "subject-2" || got.ClaimSets != nil {
		t.Fatalf("identifier provider = %#v", got)
	}
}

func TestOIDCProviderNicknameCandidatesUseExplicitProviderPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		provider OIDCProviderCredential
		want     []string
	}{
		{
			name: "google name then given family",
			provider: OIDCProviderCredential{Provider: "google", ClaimSets: []OIDCProviderClaims{{
				Name: "Display Name", GivenName: "Given", FamilyName: "Family",
			}}},
			want: []string{"Display Name", "Given Family"},
		},
		{
			name: "github name then login then preferred username",
			provider: OIDCProviderCredential{Provider: "github", ClaimSets: []OIDCProviderClaims{{
				Name: "Display Name", Login: "octocat", PreferredUsername: "preferred-octocat",
			}}},
			want: []string{"Display Name", "octocat", "preferred-octocat"},
		},
		{
			name: "generic is explicitly supported",
			provider: OIDCProviderCredential{Provider: "generic", ClaimSets: []OIDCProviderClaims{{
				Name: "Display Name", PreferredUsername: "preferred", Nickname: "nickname", GivenName: "Given", FamilyName: "Family",
			}}},
			want: []string{"Display Name", "preferred", "nickname", "Given Family"},
		},
		{
			name:     "unknown provider is not a generic fallback",
			provider: OIDCProviderCredential{Provider: "unreviewed", ClaimSets: []OIDCProviderClaims{{Name: "Ignored"}}},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.provider.NicknameCandidates(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NicknameCandidates() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCredentialInventoryReadsNicknameClaimsFromKratosOIDCCredential(t *testing.T) {
	inventory := NewCredentialInventory(map[string]Credential{
		"oidc": {Type: "oidc", Config: structured.Fields{"providers": structured.Values{structured.Fields{
			"provider": "google",
			"subject":  "subject-1",
			"claims": structured.Fields{
				"name": "Claim Display Name", "given_name": "Given", "family_name": "Family",
			},
		}}}},
	})

	providers := inventory.OIDCProviders()
	if len(providers) != 1 {
		t.Fatalf("OIDCProviders() = %#v, want one provider", providers)
	}
	if got, want := providers[0].NicknameCandidates(), []string{"Claim Display Name", "Given Family"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NicknameCandidates() = %#v, want %#v", got, want)
	}
}

func TestCredentialInventorySkipsMalformedNicknameClaimValues(t *testing.T) {
	inventory := NewCredentialInventory(map[string]Credential{
		"oidc": {Type: "oidc", Config: structured.Fields{"providers": structured.Values{structured.Fields{
			"provider": "google",
			"subject":  "subject-1",
			"claims": structured.Fields{
				"name": structured.Values{"not", "a", "string"}, "given_name": "Given", "family_name": "Family",
			},
		}}}},
	})

	providers := inventory.OIDCProviders()
	if len(providers) != 1 {
		t.Fatalf("OIDCProviders() = %#v, want one provider", providers)
	}
	if got, want := providers[0].NicknameCandidates(), []string{"Given Family"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NicknameCandidates() = %#v, want %#v", got, want)
	}
}

func TestCredentialInventoryMatchesCanonicalOIDCProvider(t *testing.T) {
	inventory := NewCredentialInventory(map[string]Credential{
		"oidc": {
			Type:        "oidc",
			Identifiers: []string{"google:google-sub", "github:github-sub"},
		},
	})

	if !inventory.HasOIDCProvider(" Google ", "google-sub") {
		t.Fatal("expected normalized provider name and subject to match")
	}
	if inventory.HasOIDCProvider("github", "other-sub") {
		t.Fatal("unexpected provider subject match")
	}
}

func TestCredentialInventoryRecoverableMethodsExcludePasskeys(t *testing.T) {
	inventory := NewCredentialInventory(map[string]Credential{
		"oidc":    {Type: "oidc", Identifiers: []string{"google:google-sub"}},
		"passkey": {Type: "passkey", Config: structured.Fields{"credentials": structured.Values{structured.Fields{"id": "key"}}}},
	})
	if got := inventory.RecoverableAuthenticationMethodCount(); got != 1 {
		t.Fatalf("recoverable method count = %d, want 1", got)
	}
	if got := inventory.RecoverableAuthenticationMethodCountAfterOIDCRemoval("google", "google-sub"); got != 0 {
		t.Fatalf("recoverable method count after removal = %d, want 0", got)
	}
}

func TestHasUsableCodeCredential(t *testing.T) {
	t.Run("identifier is usable without confidential config", func(t *testing.T) {
		if !HasUsableCodeCredential(Credential{Type: "code", Identifiers: []string{"user@example.com"}}) {
			t.Fatal("expected code identifier to be usable")
		}
	})
	t.Run("address config is usable", func(t *testing.T) {
		credential := Credential{
			Type: "code",
			Config: structured.Fields{
				"addresses": structured.Values{
					structured.Fields{"channel": "email", "address": "user@example.com"},
				},
			},
		}
		if !HasUsableCodeCredential(credential) {
			t.Fatal("expected code address to be usable")
		}
	})
	t.Run("empty shell is not usable", func(t *testing.T) {
		if HasUsableCodeCredential(Credential{Type: "code"}) {
			t.Fatal("expected empty code credential shell to be unusable")
		}
	})
}

func TestCodeCredentialHasAddress(t *testing.T) {
	t.Run("matches identifier case insensitively", func(t *testing.T) {
		credential := Credential{Type: "code", Identifiers: []string{"User@Example.com"}}

		if !CodeCredentialHasAddress(credential, " user@example.com ") {
			t.Fatal("expected code identifier to match recipient")
		}
	})
	t.Run("matches configured address", func(t *testing.T) {
		credential := Credential{
			Type: "code",
			Config: structured.Fields{
				"addresses": structured.Values{
					structured.Fields{"channel": "email", "address": "user@example.com"},
				},
			},
		}

		if !CodeCredentialHasAddress(credential, "user@example.com") {
			t.Fatal("expected configured code address to match recipient")
		}
	})
	t.Run("rejects a different address", func(t *testing.T) {
		credential := Credential{Type: "code", Identifiers: []string{"other@example.com"}}

		if CodeCredentialHasAddress(credential, "user@example.com") {
			t.Fatal("expected mismatched code address to be rejected")
		}
	})
}
