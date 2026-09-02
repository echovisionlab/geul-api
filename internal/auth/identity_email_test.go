package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

func TestIdentityCurrentEmailVerified(t *testing.T) {
	tests := []struct {
		name     string
		identity *Identity
		want     bool
	}{
		{
			name: "current email has matching verified address",
			identity: &Identity{
				Traits: structured.Fields{"email": "User@Example.com"},
				VerifiableAddresses: []VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: true},
				},
			},
			want: true,
		},
		{
			name: "matching address is unverified",
			identity: &Identity{
				Traits: structured.Fields{"email": "user@example.com"},
				VerifiableAddresses: []VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: false},
				},
			},
			want: false,
		},
		{
			name: "different verified address is not enough",
			identity: &Identity{
				Traits: structured.Fields{"email": "current@example.com"},
				VerifiableAddresses: []VerifiableAddress{
					{Via: "email", Value: "other@example.com", Verified: true},
				},
			},
			want: false,
		},
		{
			name: "missing email trait",
			identity: &Identity{
				Traits: structured.Fields{},
				VerifiableAddresses: []VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: true},
				},
			},
			want: false,
		},
		{
			name: "legacy local synthetic email is not deliverable",
			identity: &Identity{
				Traits: structured.Fields{"email": "user.google.local"},
				VerifiableAddresses: []VerifiableAddress{
					{Via: "email", Value: "user.google.local", Verified: true},
				},
			},
			want: false,
		},
		{
			name:     "nil identity",
			identity: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.identity.CurrentEmailVerified(); got != tt.want {
				t.Fatalf("CurrentEmailVerified() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdentityPendingAndUnverifiedEmail(t *testing.T) {
	identity := &Identity{
		Traits: structured.Fields{
			"email":         "current@example.test",
			"pending_email": " Pending@Example.test ",
		},
		VerifiableAddresses: []VerifiableAddress{
			{Via: "email", Value: "current@example.test", Verified: true},
			{Via: "email", Value: "pending@example.test", Verified: false},
		},
	}

	if got := identity.PendingEmail(); got != "Pending@Example.test" {
		t.Fatalf("PendingEmail() = %q", got)
	}
	if !identity.HasUnverifiedEmailAddress("pending@example.test") {
		t.Fatal("expected pending address to be recognized as unverified")
	}
	if identity.HasUnverifiedEmailAddress("current@example.test") {
		t.Fatal("expected verified current address not to be recognized as unverified")
	}
	if identity.HasUnverifiedEmailAddress("missing@example.test") {
		t.Fatal("expected unknown address not to be recognized as unverified")
	}
}

func TestNilIdentityEmailProofChecksAreSafe(t *testing.T) {
	var identity *Identity

	require.Nil(t, identity.GetTraitString("email"))
	require.Nil(t, identity.GetTraitMap("profile"))
	require.False(t, identity.IsBanned())
	require.Nil(t, identity.GetBanReason())
	require.False(t, identity.HasVerifiedEmailAddress("user@example.test"))
	require.False(t, identity.HasUnverifiedEmailAddress("user@example.test"))
}

func TestDeleteIdentityIsIdempotentWhenKratosIdentityIsMissing(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusNoContent, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/admin/identities/identity-1" {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)

			require.NoError(t, NewKratosClient(server.URL).DeleteIdentity(t.Context(), "identity-1"))
		})
	}
}

func TestDeleteIdentitySessionsIsIdempotentWhenNoSessionsExist(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/admin/identities/identity-1/sessions" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)

			require.NoError(t, NewKratosClient(server.URL).DeleteIdentitySessions(t.Context(), "identity-1"))
		})
	}
}

func TestDeleteIdentityRejectsUnexpectedKratosResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	err := NewKratosClient(server.URL).DeleteIdentity(t.Context(), "identity-1")
	require.ErrorContains(t, err, "kratos returned 503")
}

func TestUpdateIdentityTraitsRejectsDirectEmailMutation(t *testing.T) {
	client := NewKratosClient("http://127.0.0.1:1")

	err := client.UpdateIdentityTraits(context.Background(), "identity-1", structured.Fields{
		"email": "new@example.com",
	})
	if err == nil {
		t.Fatal("expected direct email trait update to be rejected")
	}
	if !strings.Contains(err.Error(), "account email lifecycle") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateIdentityTraitsRejectsPendingEmailMutation(t *testing.T) {
	client := NewKratosClient("http://127.0.0.1:1")

	err := client.UpdateIdentityTraits(context.Background(), "identity-1", structured.Fields{
		"pending_email": "new@example.com",
	})
	require.ErrorContains(t, err, "account email lifecycle")
}

func TestUpdateIdentityTraitsDoesNotReplayVerifiableAddressSnapshot(t *testing.T) {
	var patch []structured.Fields
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/identities/identity-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"identity-1",
				"traits":{"email":"old@example.test","name":"Old"},
				"verifiable_addresses":[{"value":"old@example.test","via":"email","verified":true}]
			}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/admin/identities/identity-1":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patch))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"identity-1","traits":{"email":"old@example.test","name":"New"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	require.NoError(t, NewKratosClient(server.URL).UpdateIdentityTraits(t.Context(), "identity-1", structured.Fields{
		"name": "New",
	}))
	require.Equal(t, []structured.Fields{{
		"op":    "add",
		"path":  "/traits/name",
		"value": "New",
	}}, patch)
}

func TestCreateIdentityUsesKratosAdminCreateIdentity(t *testing.T) {
	var gotContentType string
	var gotCreate structured.Fields
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != "/admin/identities" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotCreate); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id": "identity-1",
			"schema_id": "user",
			"state": "active",
			"traits": {"email": "admin@example.test"}
		}`))
	}))
	defer server.Close()

	client := NewKratosClient(server.URL)
	created, err := client.CreateIdentity(context.Background(), &Identity{
		SchemaID: "user",
		State:    "",
		Traits: structured.Fields{
			"email": "admin@example.test",
			"name":  "Admin",
		},
		MetadataPublic: structured.Fields{"display_name": "Admin"},
		MetadataAdmin:  structured.Fields{"banned": false},
		VerifiableAddresses: []VerifiableAddress{
			{Via: "email", Value: "admin@example.test", Verified: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateIdentity() error = %v", err)
	}
	if created.ID != "identity-1" {
		t.Fatalf("created.ID = %q", created.ID)
	}

	if gotContentType != "application/json" {
		t.Fatalf("content type = %q", gotContentType)
	}
	if gotCreate["schema_id"] != "user" {
		t.Fatalf("schema_id = %#v", gotCreate["schema_id"])
	}
	if gotCreate["state"] != KratosStateActive {
		t.Fatalf("state = %#v", gotCreate["state"])
	}
	traits, ok := gotCreate["traits"].(structured.Fields)
	if !ok || traits["email"] != "admin@example.test" || traits["name"] != "Admin" {
		t.Fatalf("traits = %#v", gotCreate["traits"])
	}
	metadataPublic, ok := gotCreate["metadata_public"].(structured.Fields)
	if !ok || metadataPublic["display_name"] != "Admin" {
		t.Fatalf("metadata_public = %#v", gotCreate["metadata_public"])
	}
	metadataAdmin, ok := gotCreate["metadata_admin"].(structured.Fields)
	if !ok || metadataAdmin["banned"] != false {
		t.Fatalf("metadata_admin = %#v", gotCreate["metadata_admin"])
	}
	addresses, ok := gotCreate["verifiable_addresses"].(structured.Values)
	if !ok || len(addresses) != 1 {
		t.Fatalf("verifiable_addresses = %#v", gotCreate["verifiable_addresses"])
	}
	if _, ok := gotCreate["credentials"]; ok {
		t.Fatalf("passwordless create unexpectedly sent credentials: %#v", gotCreate["credentials"])
	}
}

func TestCreateIdentityValidatesRequiredInput(t *testing.T) {
	client := NewKratosClient("http://127.0.0.1:1")

	tests := []struct {
		name     string
		identity *Identity
		want     string
	}{
		{name: "nil identity", identity: nil, want: "identity is required"},
		{name: "missing schema", identity: &Identity{Traits: structured.Fields{}}, want: "identity schema_id is required"},
		{name: "missing traits", identity: &Identity{SchemaID: "user"}, want: "identity traits are required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.CreateIdentity(context.Background(), tt.identity)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateIdentity() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDeleteIdentityCredentialIdentifierUsesIdentifierQuery(t *testing.T) {
	var gotRequest string
	var gotIdentifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequest = r.Method + " " + r.URL.Path
		gotIdentifier = r.URL.Query().Get("identifier")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewKratosClient(server.URL)
	err := client.DeleteIdentityCredentialIdentifier(context.Background(), "identity-1", "oidc", "google:subject 1")
	if err != nil {
		t.Fatalf("DeleteIdentityCredentialIdentifier() error = %v", err)
	}

	if gotRequest != "DELETE /admin/identities/identity-1/credentials/oidc" {
		t.Fatalf("request = %q", gotRequest)
	}
	if gotIdentifier != "google:subject 1" {
		t.Fatalf("identifier = %q", gotIdentifier)
	}
}

func TestDeleteIdentityCredentialIdentifierRejectsMissingIdentifier(t *testing.T) {
	client := NewKratosClient("http://127.0.0.1:1")
	err := client.DeleteIdentityCredentialIdentifier(context.Background(), "identity-1", "oidc", " ")
	if err == nil || !strings.Contains(err.Error(), "credential identifier is required") {
		t.Fatalf("DeleteIdentityCredentialIdentifier() error = %v", err)
	}
}

func TestDeleteIdentityCredentialIdentifierReturnsKratosError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewKratosClient(server.URL)
	err := client.DeleteIdentityCredentialIdentifier(context.Background(), "identity-1", "oidc", "google:subject")
	if err == nil || !strings.Contains(err.Error(), "kratos returned 400") {
		t.Fatalf("DeleteIdentityCredentialIdentifier() error = %v", err)
	}
}

func TestFindIdentityByCredentialIdentifier(t *testing.T) {
	t.Run("returns the one exact match", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Path; got != "/admin/identities" {
				t.Fatalf("path = %q", got)
			}
			if got := r.URL.Query().Get("credentials_identifier"); got != "johndoe@example.com" {
				t.Fatalf("credentials_identifier = %q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "2" {
				t.Fatalf("page_size = %q", got)
			}
			_ = json.NewEncoder(w).Encode([]structured.Fields{{
				"id":        "identity-1",
				"schema_id": "user",
				"traits":    structured.Fields{"email": "johndoe@example.com"},
			}})
		}))
		defer server.Close()

		identity, found, err := NewKratosClient(server.URL).
			FindIdentityByCredentialIdentifier(context.Background(), " JohnDoe@Example.com ")
		if err != nil {
			t.Fatalf("FindIdentityByCredentialIdentifier() error = %v", err)
		}
		if !found || identity == nil || identity.ID != "identity-1" {
			t.Fatalf("identity = %#v, found = %v", identity, found)
		}
	})

	t.Run("treats no match as a normal result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(structured.Values{})
		}))
		defer server.Close()

		identity, found, err := NewKratosClient(server.URL).
			FindIdentityByCredentialIdentifier(context.Background(), "new@example.com")
		if err != nil || found || identity != nil {
			t.Fatalf("identity = %#v, found = %v, error = %v", identity, found, err)
		}
	})

	t.Run("rejects an ambiguous credential invariant", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]structured.Fields{{"id": "identity-1"}, {"id": "identity-2"}})
		}))
		defer server.Close()

		_, _, err := NewKratosClient(server.URL).
			FindIdentityByCredentialIdentifier(context.Background(), "duplicate@example.com")
		if err == nil || !strings.Contains(err.Error(), "multiple identities") {
			t.Fatalf("error = %v", err)
		}
	})
}
