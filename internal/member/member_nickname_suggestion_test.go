package member

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFetchMemberNicknameSuggestionUsesProviderSpecificPublicName(t *testing.T) {
	originalClient := memberNicknameSuggestionHTTPClient
	originalGoogle := memberGoogleUserInfoEndpoint
	originalGitHub := memberGitHubUserEndpoint
	t.Cleanup(func() {
		memberNicknameSuggestionHTTPClient = originalClient
		memberGoogleUserInfoEndpoint = originalGoogle
		memberGitHubUserEndpoint = originalGitHub
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer provider-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/google":
			_, _ = w.Write([]byte(`{"given_name":"Gildong","family_name":"Hong"}`))
		case "/github":
			require.Equal(t, "geul-backend", r.Header.Get("User-Agent"))
			_, _ = w.Write([]byte(`{"login":"octocat","name":"Ignored Name"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	memberNicknameSuggestionHTTPClient = server.Client()
	memberGoogleUserInfoEndpoint = server.URL + "/google"
	memberGitHubUserEndpoint = server.URL + "/github"

	google, err := fetchMemberNicknameSuggestion(t.Context(), "google", "provider-token")
	require.NoError(t, err)
	require.Equal(t, "Gildong Hong", google)

	github, err := fetchMemberNicknameSuggestion(t.Context(), "github", "provider-token")
	require.NoError(t, err)
	require.Equal(t, "Ignored Name", github)
}

func TestMemberNicknameSuggestionPrefersClaimsAndSkipsInvalidClaimValues(t *testing.T) {
	originalClient := memberNicknameSuggestionHTTPClient
	originalGoogle := memberGoogleUserInfoEndpoint
	t.Cleanup(func() {
		memberNicknameSuggestionHTTPClient = originalClient
		memberGoogleUserInfoEndpoint = originalGoogle
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "claim should have been preferred", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	memberNicknameSuggestionHTTPClient = server.Client()
	memberGoogleUserInfoEndpoint = server.URL

	const identityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const memberID = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Credentials: map[string]auth.Credential{"oidc": {
			Type: "oidc",
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": "google", "subject": "subject", "initial_access_token": "provider-token",
				"claims": structured.Fields{
					"name": strings.Repeat("x", memberNicknameMaxLength+1), "given_name": "Gildong", "family_name": "Hong",
				},
			}}},
		}},
	}
	service := &MemberService{identity: &fakeIdentityManager{identity: identity}}
	member := model.Member{ID: memberID, AccountIdentityID: new(identityID)}
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, MemberID: memberID,
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})

	suggestion := service.nicknameSuggestion(ctx, member)
	require.NotNil(t, suggestion)
	require.Equal(t, "Gildong Hong", *suggestion)
	require.Zero(t, requests, "usable claim must avoid the live provider lookup")
}

func TestMemberNicknameSuggestionIsLiveBestEffortOnlyBeforeOnboarding(t *testing.T) {
	originalClient := memberNicknameSuggestionHTTPClient
	originalGoogle := memberGoogleUserInfoEndpoint
	t.Cleanup(func() {
		memberNicknameSuggestionHTTPClient = originalClient
		memberGoogleUserInfoEndpoint = originalGoogle
	})

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"given_name":"Gildong","family_name":"Hong"}`))
	}))
	t.Cleanup(server.Close)
	memberNicknameSuggestionHTTPClient = server.Client()
	memberGoogleUserInfoEndpoint = server.URL

	const identityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const memberID = "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb"
	identity := &auth.Identity{
		ID:         identityID,
		ExternalID: memberID,
		Credentials: map[string]auth.Credential{"oidc": {
			Type: "oidc",
			Config: structured.Fields{"providers": structured.Values{structured.Fields{
				"provider": "google", "subject": "subject", "initial_access_token": "token",
			}}},
		}},
	}
	service := &MemberService{identity: &fakeIdentityManager{identity: identity}}
	member := model.Member{ID: memberID, AccountIdentityID: new(identityID)}
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: identityID, MemberID: memberID,
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})

	suggestion := service.nicknameSuggestion(ctx, member)
	require.NotNil(t, suggestion)
	require.Equal(t, "Gildong Hong", *suggestion)
	require.Equal(t, 1, requests)

	member.Onboarded = true
	require.Nil(t, service.nicknameSuggestion(ctx, member))
	require.Equal(t, 1, requests, "onboarded Member must not query the provider")
}
