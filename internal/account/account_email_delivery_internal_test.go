package account

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestIdentityHasUsableDeliveryEmailRefreshesLegacyProviderAssertion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer provider-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"email":"provider@example.com","email_verified":true}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	previousEndpoint := googleUserInfoEndpoint
	googleUserInfoEndpoint = server.URL
	t.Cleanup(func() { googleUserInfoEndpoint = previousEndpoint })

	identity := &auth.Identity{
		Traits: structured.Fields{"email": "provider@example.com"},
		Credentials: map[string]auth.Credential{
			"oidc": {
				Type: "oidc",
				Config: structured.Fields{"providers": structured.Values{structured.Fields{
					"provider":             "google",
					"subject":              "provider-subject",
					"initial_access_token": "provider-token",
				}}},
			},
		},
	}

	require.True(t, IdentityHasUsableDeliveryEmail(t.Context(), identity, "provider@example.com"))
}
