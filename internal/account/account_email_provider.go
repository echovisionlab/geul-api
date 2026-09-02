package account

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var (
	providerEmailLookupTimeout = 3 * time.Second
	providerEmailHTTPClient    = &http.Client{Timeout: providerEmailLookupTimeout}
	googleUserInfoEndpoint     = "https://openidconnect.googleapis.com/v1/userinfo"
	githubEmailsEndpoint       = "https://api.github.com/user/emails"
)

type googleAccountEmailResponse struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

type githubAccountEmailResponse struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func fetchProviderEmailJSON(
	ctx context.Context,
	provider string,
	endpoint string,
	accessToken string,
	destination structured.Value,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if auth.NormalizeOIDCProvider(provider) == "github" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", sharedtelemetry.ServiceBackend.String())
	}

	resp, err := providerEmailHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("status=%d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(destination); err != nil {
		return err
	}
	return nil
}

func fetchGoogleAccountEmail(ctx context.Context, accessToken string) (string, error) {
	var payload googleAccountEmailResponse
	if err := fetchProviderEmailJSON(ctx, "google", googleUserInfoEndpoint, accessToken, &payload); err != nil {
		return "", err
	}
	if !payload.EmailVerified || !auth.ValidRealAccountEmail(payload.Email) {
		return "", nil
	}
	return strings.TrimSpace(payload.Email), nil
}

func fetchGitHubAccountEmail(ctx context.Context, accessToken string) (string, error) {
	var payload []githubAccountEmailResponse
	if err := fetchProviderEmailJSON(ctx, "github", githubEmailsEndpoint, accessToken, &payload); err != nil {
		return "", err
	}
	for _, email := range payload {
		if email.Primary && email.Verified && auth.ValidRealAccountEmail(email.Email) {
			return strings.TrimSpace(email.Email), nil
		}
	}
	return "", nil
}

func fetchAccountEmailFromProvider(ctx context.Context, provider, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", nil
	}
	switch auth.NormalizeOIDCProvider(provider) {
	case "google":
		return fetchGoogleAccountEmail(ctx, accessToken)
	case "github":
		return fetchGitHubAccountEmail(ctx, accessToken)
	default:
		return "", nil
	}
}

// ResolveAccountEmailProviderCandidates derives verified email candidates from
// the exact connected OIDC credentials. Embedded verified claims are preferred;
// GitHub and legacy Google credentials fall back to their provider endpoint.
func ResolveAccountEmailProviderCandidates(
	ctx context.Context,
	credentials map[string]auth.Credential,
) []AccountEmailProviderCandidate {
	candidates := []AccountEmailProviderCandidate{}
	for _, credential := range auth.NewCredentialInventory(credentials).OIDCProviders() {
		provider := credential.Provider
		if provider != "google" && provider != "github" && provider != "apple" {
			continue
		}
		email := credential.VerifiedAccountEmail()
		if email == "" {
			lookupCtx, cancel := context.WithTimeout(ctx, providerEmailLookupTimeout)
			resolvedEmail, err := fetchAccountEmailFromProvider(lookupCtx, provider, credential.InitialAccessToken)
			cancel()
			if err != nil {
				slog.Warn("Failed to resolve provider email", "error", err, "provider", provider)
			}
			email = resolvedEmail
		}
		if email == "" {
			continue
		}
		candidates = append(candidates, AccountEmailProviderCandidate{
			Provider:        provider,
			ProviderSubject: credential.Subject,
			Email:           email,
			Verified:        true,
		})
	}
	return candidates
}
