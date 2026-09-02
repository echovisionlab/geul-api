package member

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const memberNicknameSuggestionTimeout = 3 * time.Second

var (
	memberNicknameSuggestionHTTPClient = &http.Client{Timeout: memberNicknameSuggestionTimeout}
	memberGoogleUserInfoEndpoint       = "https://openidconnect.googleapis.com/v1/userinfo"
	memberGitHubUserEndpoint           = "https://api.github.com/user"
)

type memberGoogleProfile struct {
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

type memberGitHubProfile struct {
	Name  string `json:"name"`
	Login string `json:"login"`
}

func fetchMemberOIDCProfile(ctx context.Context, provider, endpoint, accessToken string, destination structured.Value) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if provider == "github" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", sharedtelemetry.ServiceBackend.String())
	}
	resp, err := memberNicknameSuggestionHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provider returned status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode provider profile: %w", err)
	}
	return nil
}

func fetchMemberNicknameSuggestion(ctx context.Context, provider, accessToken string) (string, error) {
	provider = auth.NormalizeOIDCProvider(provider)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", nil
	}
	switch provider {
	case "google":
		var profile memberGoogleProfile
		if err := fetchMemberOIDCProfile(ctx, provider, memberGoogleUserInfoEndpoint, accessToken, &profile); err != nil {
			return "", err
		}
		if name := strings.TrimSpace(profile.Name); name != "" {
			return name, nil
		}
		return strings.TrimSpace(strings.TrimSpace(profile.GivenName) + " " + strings.TrimSpace(profile.FamilyName)), nil
	case "github":
		var profile memberGitHubProfile
		if err := fetchMemberOIDCProfile(ctx, provider, memberGitHubUserEndpoint, accessToken, &profile); err != nil {
			return "", err
		}
		if name := strings.TrimSpace(profile.Name); name != "" {
			return name, nil
		}
		return strings.TrimSpace(profile.Login), nil
	default:
		return "", nil
	}
}

func (s *MemberService) nicknameSuggestion(ctx context.Context, member model.Member) *string {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || member.Onboarded || member.AccountIdentityID == nil ||
		*member.AccountIdentityID != principal.IdentityID.String() || member.ID != principal.MemberID.String() {
		return nil
	}
	identity, err := s.identity.GetIdentityWithIncludeCredential(ctx, principal.IdentityID.String(), "oidc")
	if err != nil || identity == nil {
		if err != nil {
			slog.Warn("OIDC nickname suggestion identity lookup failed", "error", err, "identity_id", principal.IdentityID.String())
		}
		return nil
	}
	if identity.ID != principal.IdentityID.String() || identity.ExternalID != principal.MemberID.String() {
		return nil
	}
	for _, provider := range auth.NewCredentialInventory(identity.Credentials).OIDCProviders() {
		for _, candidate := range provider.NicknameCandidates() {
			suggestion, normalizeErr := normalizeMemberNickname(candidate)
			if normalizeErr == nil {
				return &suggestion
			}
		}

		lookupCtx, cancel := context.WithTimeout(ctx, memberNicknameSuggestionTimeout)
		suggestion, lookupErr := fetchMemberNicknameSuggestion(lookupCtx, provider.Provider, provider.InitialAccessToken)
		cancel()
		if lookupErr != nil {
			slog.Warn("OIDC nickname suggestion lookup failed", "error", lookupErr, "provider", provider.Provider, "identity_id", principal.IdentityID.String())
			continue
		}
		suggestion, normalizeErr := normalizeMemberNickname(suggestion)
		if normalizeErr == nil {
			return &suggestion
		}
	}
	return nil
}
