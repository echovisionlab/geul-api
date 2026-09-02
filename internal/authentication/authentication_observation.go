package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type authenticationObservationKey struct{}

type authenticationObservation struct {
	Candidate      bool
	OIDCCallback   bool
	FlowKind       sharedtelemetry.AuthenticationFlowKind
	Method         sharedtelemetry.AuthenticationMethod
	Provider       string
	ProofSubmitted bool
	FacadeBlock    sharedtelemetry.AuthenticationBlockReason
	IncomingMember *auth.UserInfo
}

func inspectAuthenticationObservation(request *http.Request) (*authenticationObservation, error) {
	observation := &authenticationObservation{}
	if request == nil {
		return observation, nil
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case request.Method == http.MethodPost && path == unifiedAuthPath:
		observation.Candidate = true
	case request.Method == http.MethodPost && path == "/self-service/login":
		observation.Candidate = true
		observation.FlowKind = sharedtelemetry.AuthenticationFlowLogin
	case request.Method == http.MethodPost && path == "/self-service/registration":
		observation.Candidate = true
		observation.FlowKind = sharedtelemetry.AuthenticationFlowRegistration
	case (request.Method == http.MethodGet || request.Method == http.MethodPost) &&
		strings.HasPrefix(path, "/self-service/methods/oidc/callback"):
		observation.Candidate = true
		observation.OIDCCallback = true
		observation.FlowKind = sharedtelemetry.AuthenticationFlowLogin
		observation.Method = sharedtelemetry.AuthenticationMethodOIDC
		observation.Provider = oidcCallbackProvider(path)
		observation.ProofSubmitted = true
		return observation, nil
	default:
		return observation, nil
	}

	body, err := readBoundedKratosBody(request.Body)
	if err != nil {
		return observation, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	method, provider, proofSubmitted := authenticationPayload(
		request.Header.Get("Content-Type"),
		body,
	)
	observation.Method = method
	observation.Provider = provider
	observation.ProofSubmitted = proofSubmitted
	return observation, nil
}

func observeUnifiedAuthentication(
	ctx context.Context,
	kind unifiedAuthFlowKind,
	refresh bool,
	contentType string,
	body []byte,
) {
	observation, ok := ctx.Value(authenticationObservationKey{}).(*authenticationObservation)
	if !ok {
		return
	}
	if refresh {
		observation.FlowKind = sharedtelemetry.AuthenticationFlowReauthentication
	} else if kind == unifiedAuthRegistrationFlow {
		observation.FlowKind = sharedtelemetry.AuthenticationFlowRegistration
	} else {
		observation.FlowKind = sharedtelemetry.AuthenticationFlowLogin
	}
	observation.Method, observation.Provider, observation.ProofSubmitted = authenticationPayload(contentType, body)
}

func markAuthenticationFacadeBlock(ctx context.Context, reason sharedtelemetry.AuthenticationBlockReason) {
	if observation, ok := ctx.Value(authenticationObservationKey{}).(*authenticationObservation); ok {
		observation.FacadeBlock = reason
	}
}

func authenticationPayload(contentType string, body []byte) (
	sharedtelemetry.AuthenticationMethod,
	string,
	bool,
) {
	var method, code, provider, passkeyLogin string
	if kratosRequestMediaType(contentType) == "application/json" {
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			return "", "", false
		}
		method, _ = payload["method"].(string)
		code, _ = payload["code"].(string)
		provider, _ = payload["provider"].(string)
		passkeyLogin, _ = payload["passkey_login"].(string)
	} else {
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return "", "", false
		}
		method = values.Get("method")
		code = values.Get("code")
		provider = values.Get("provider")
		passkeyLogin = values.Get("passkey_login")
	}
	method = strings.ToLower(strings.TrimSpace(method))
	provider = boundedAuthenticationProvider(provider)
	switch {
	case strings.TrimSpace(passkeyLogin) != "":
		return sharedtelemetry.AuthenticationMethodPasskey, "", true
	case provider != "" || method == "oidc":
		return sharedtelemetry.AuthenticationMethodOIDC, provider, false
	case method == "code":
		return sharedtelemetry.AuthenticationMethodEmailCode, "", strings.TrimSpace(code) != ""
	default:
		return "", "", false
	}
}

var boundedAuthenticationProviderPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func boundedAuthenticationProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !boundedAuthenticationProviderPattern.MatchString(provider) {
		return ""
	}
	return provider
}

func oidcCallbackProvider(path string) string {
	prefix := "/self-service/methods/oidc/callback/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return boundedAuthenticationProvider(strings.TrimPrefix(path, prefix))
}

func authenticationResponseIsIntermediate(
	observation *authenticationObservation,
	response bufferedKratosResponse,
) bool {
	if observation == nil || observation.OIDCCallback || observation.ProofSubmitted || observation.FacadeBlock != "" {
		return false
	}
	switch observation.Method {
	case sharedtelemetry.AuthenticationMethodEmailCode:
		return response.StatusCode < http.StatusBadRequest || jsonBodyContains(response.Body, "state", "sent_email")
	case sharedtelemetry.AuthenticationMethodOIDC, sharedtelemetry.AuthenticationMethodPasskey:
		return response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest ||
			kratosErrorID(response.Body) == "browser_location_change_required"
	default:
		return false
	}
}
