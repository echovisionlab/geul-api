package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestResolveAccountEmailProviderCandidatesUsesProviderSources(t *testing.T) {
	originalHTTPClient := providerEmailHTTPClient
	originalGitHubEndpoint := githubEmailsEndpoint
	originalTimeout := providerEmailLookupTimeout
	t.Cleanup(func() {
		providerEmailHTTPClient = originalHTTPClient
		githubEmailsEndpoint = originalGitHubEndpoint
		providerEmailLookupTimeout = originalTimeout
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer github-token", r.Header.Get("Authorization"))
		require.Equal(t, "/user/emails", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode([]githubAccountEmailResponse{
			{Email: " GitHub@Example.Test ", Primary: true, Verified: false},
			{Email: " GitHubVerified@Example.Test ", Primary: true, Verified: true},
			{Email: "ignored-secondary@example.test", Primary: false, Verified: true},
		}))
	}))
	t.Cleanup(server.Close)
	providerEmailHTTPClient = server.Client()
	githubEmailsEndpoint = server.URL + "/user/emails"
	providerEmailLookupTimeout = time.Second

	candidates := ResolveAccountEmailProviderCandidates(context.Background(), map[string]auth.Credential{
		"oidc": {
			Type: "oidc",
			Config: structured.Fields{"providers": structured.Values{
				structured.Fields{"provider": "google", "subject": "google-subject", "email": " Google@Example.Test ", "email_verified": true},
				structured.Fields{"provider": "google", "subject": "google-unverified", "email": "unverified-google@example.test", "email_verified": false},
				structured.Fields{"provider": "github", "subject": "github-subject", "initial_access_token": "github-token"},
				structured.Fields{"provider": "naver", "subject": "naver-subject", "email": "naver@example.test", "email_verified": true},
				structured.Fields{"provider": "google", "subject": "synthetic-subject", "email": "synthetic@google.local", "email_verified": true},
			}},
		},
	})

	require.Equal(t, []AccountEmailProviderCandidate{
		{Provider: "google", ProviderSubject: "google-subject", Email: "Google@Example.Test", Verified: true},
		{Provider: "github", ProviderSubject: "github-subject", Email: "GitHubVerified@Example.Test", Verified: true},
	}, candidates)
}

func TestFetchGoogleAccountEmailRequiresRealEmail(t *testing.T) {
	originalHTTPClient := providerEmailHTTPClient
	originalGoogleEndpoint := googleUserInfoEndpoint
	t.Cleanup(func() {
		providerEmailHTTPClient = originalHTTPClient
		googleUserInfoEndpoint = originalGoogleEndpoint
	})

	responses := map[string]struct {
		status int
		body   googleAccountEmailResponse
	}{
		"/verified":   {status: http.StatusOK, body: googleAccountEmailResponse{Email: " GoogleUser@Example.Test ", EmailVerified: true}},
		"/unverified": {status: http.StatusOK, body: googleAccountEmailResponse{Email: "unverified@example.test", EmailVerified: false}},
		"/synthetic":  {status: http.StatusOK, body: googleAccountEmailResponse{Email: "synthetic@google.local", EmailVerified: true}},
		"/error":      {status: http.StatusServiceUnavailable, body: googleAccountEmailResponse{Email: "ignored@example.test", EmailVerified: true}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer google-token", r.Header.Get("Authorization"))
		response, ok := responses[r.URL.Path]
		require.True(t, ok, "unexpected path %s", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.status)
		require.NoError(t, json.NewEncoder(w).Encode(response.body))
	}))
	t.Cleanup(server.Close)
	providerEmailHTTPClient = server.Client()

	googleUserInfoEndpoint = server.URL + "/verified"
	email, err := fetchGoogleAccountEmail(context.Background(), "google-token")
	require.NoError(t, err)
	require.Equal(t, "GoogleUser@Example.Test", email)

	googleUserInfoEndpoint = server.URL + "/unverified"
	email, err = fetchGoogleAccountEmail(context.Background(), "google-token")
	require.NoError(t, err)
	require.Empty(t, email)

	googleUserInfoEndpoint = server.URL + "/synthetic"
	email, err = fetchGoogleAccountEmail(context.Background(), "google-token")
	require.NoError(t, err)
	require.Empty(t, email)

	googleUserInfoEndpoint = server.URL + "/error"
	email, err = fetchGoogleAccountEmail(context.Background(), "google-token")
	require.Error(t, err)
	require.Empty(t, email)
}
