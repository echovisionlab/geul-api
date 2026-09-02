package authentication

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
)

func (recorder *AuthenticationAccessRecorder) resolveIncomingMember(request *http.Request) *auth.UserInfo {
	sessionID, err := recorder.sessionIDFromWhoAmI(request.Context(), request.Cookies())
	if err != nil || sessionID == "" {
		return nil
	}
	principal, err := recorder.resolvePrincipal(request.Context(), sessionID)
	if err != nil || principal == nil || principal.Banned {
		return nil
	}
	return principal
}

func (recorder *AuthenticationAccessRecorder) sessionIDFromTerminalResponse(
	request *http.Request,
	observation *authenticationObservation,
	response bufferedKratosResponse,
) (string, error) {
	var payload struct {
		Session *struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	_ = json.Unmarshal(response.Body, &payload)
	if payload.Session != nil && strings.TrimSpace(payload.Session.ID) != "" {
		return strings.TrimSpace(payload.Session.ID), nil
	}
	if response.StatusCode >= http.StatusBadRequest {
		return "", nil
	}
	responseCookies := (&http.Response{Header: response.Header}).Cookies()
	if len(responseCookies) == 0 &&
		observation.FlowKind != sharedtelemetry.AuthenticationFlowReauthentication && !observation.OIDCCallback {
		return "", nil
	}
	return recorder.sessionIDFromWhoAmI(
		request.Context(),
		mergeCookies(request.Cookies(), responseCookies),
	)
}

func (recorder *AuthenticationAccessRecorder) sessionIDFromWhoAmI(
	ctx context.Context,
	cookies []*http.Cookie,
) (string, error) {
	whoAmIURL := recorder.target.ResolveReference(&url.URL{Path: "/sessions/whoami"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, whoAmIURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	sharedtelemetry.InjectCorrelation(ctx, propagation.HeaderCarrier(request.Header))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response, err := recorder.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return "", nil
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kratos whoami returned status %d", response.StatusCode)
	}
	var session struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxKratosProxyRequestBytes)).Decode(&session); err != nil {
		return "", err
	}
	if !session.Active {
		return "", nil
	}
	return strings.TrimSpace(session.ID), nil
}

func firstIssuedIdentitySession(ctx context.Context, db *gorm.DB, sessionID string) (bool, error) {
	var first bool
	result := db.WithContext(ctx).Raw(`
		SELECT NOT EXISTS (
			SELECT 1
			FROM kratos.sessions AS other
			JOIN kratos.sessions AS current ON current.id = ?::uuid
			WHERE other.identity_id = current.identity_id
			  AND other.id <> current.id
			  AND other.created_at <= current.created_at
		) AS first
	`, sessionID).Scan(&first)
	return first, result.Error
}

func kratosErrorID(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	errorObject, _ := payload["error"].(map[string]any)
	errorID, _ := errorObject["id"].(string)
	return strings.TrimSpace(errorID)
}

func jsonBodyContains(body []byte, key, expected string) bool {
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return jsonTreeContainsString(payload, key, expected)
}

func bufferedAuthenticationError(status int, message string) bufferedKratosResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": message}})
	return bufferedKratosResponse{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}
