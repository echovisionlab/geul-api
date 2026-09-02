package authentication

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

const (
	testMCPAdmissionSecret    = "mcp-admission-secret"
	testMCPAuthHeaderName     = "X-Authenticated-Context-B64"
	testMCPInternalHeaderName = "X-Internal-Service"
	testMCPAdmissionIdentity  = "11111111-1111-4111-8111-11111111111a"
)

type gatewayAdmissionCheckerFunc func(context.Context, policyv1.AuthorizationDecision) (bool, error)

func (function gatewayAdmissionCheckerFunc) Can(
	ctx context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	return function(ctx, decision)
}

type nilGatewayAdmissionChecker struct{}

func (*nilGatewayAdmissionChecker) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) {
	panic("typed nil gateway admission checker must not be called")
}

func TestMCPGatewayAuthorAdmissionRequiresTrustedCanonicalInput(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		mutate func(*http.Request)
		status int
	}{
		{name: "missing internal service", mutate: func(request *http.Request) { request.Header.Del(testMCPInternalHeaderName) }, status: http.StatusUnauthorized},
		{name: "wrong internal service", mutate: func(request *http.Request) { request.Header.Set(testMCPInternalHeaderName, "wrong") }, status: http.StatusUnauthorized},
		{name: "duplicate internal service", mutate: func(request *http.Request) { request.Header.Add(testMCPInternalHeaderName, testMCPAdmissionSecret) }, status: http.StatusUnauthorized},
		{name: "bearer", mutate: func(request *http.Request) { request.Header.Set("Authorization", "Bearer caller") }, status: http.StatusUnauthorized},
		{name: "cookie", mutate: func(request *http.Request) { request.Header.Set("Cookie", "session=caller") }, status: http.StatusUnauthorized},
		{name: "session", mutate: func(request *http.Request) { request.Header.Set("X-Session-Id", testMCPAdmissionIdentity) }, status: http.StatusUnauthorized},
		{name: "member", mutate: func(request *http.Request) {
			request.Header.Set("X-Member-Id", testMCPAdmissionIdentity)
		}, status: http.StatusUnauthorized},
		{name: "role", mutate: func(request *http.Request) { request.Header.Set("X-Role", "ADMIN") }, status: http.StatusUnauthorized},
		{name: "permission", mutate: func(request *http.Request) { request.Header.Set("X-Permission", "is_admin") }, status: http.StatusUnauthorized},
		{name: "configured auth context", mutate: func(request *http.Request) {
			request.Header.Set(testMCPAuthHeaderName, "spoofed-context")
		}, status: http.StatusUnauthorized},
		{name: "empty body", body: `{}`, status: http.StatusBadRequest},
		{name: "missing identity", body: `{"role":"AUTHOR"}`, status: http.StatusBadRequest},
		{name: "extra member", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `","member_id":"` + testMCPAdmissionIdentity + `"}`, status: http.StatusBadRequest},
		{name: "extra session", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `","session_id":"` + testMCPAdmissionIdentity + `"}`, status: http.StatusBadRequest},
		{name: "extra role", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `","role":"AUTHOR"}`, status: http.StatusBadRequest},
		{name: "extra permission", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `","permission":"is_author"}`, status: http.StatusBadRequest},
		{name: "duplicate identity", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `","account_identity_id":"` + testMCPAdmissionIdentity + `"}`, status: http.StatusBadRequest},
		{name: "malformed identity", body: `{"account_identity_id":"not-a-uuid"}`, status: http.StatusBadRequest},
		{name: "noncanonical identity", body: `{"account_identity_id":"` + strings.ToUpper(testMCPAdmissionIdentity) + `"}`, status: http.StatusBadRequest},
		{name: "zero identity", body: `{"account_identity_id":"00000000-0000-0000-0000-000000000000"}`, status: http.StatusBadRequest},
		{name: "trailing json", body: `{"account_identity_id":"` + testMCPAdmissionIdentity + `"}{}`, status: http.StatusBadRequest},
		{name: "oversized", body: `{"account_identity_id":"` + strings.Repeat("a", 300) + `"}`, status: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := 0
			handler := newTestMCPGatewayAdmissionHandler(t, gatewayAdmissionCheckerFunc(
				func(context.Context, policyv1.AuthorizationDecision) (bool, error) {
					checks++
					return true, nil
				},
			))
			body := test.body
			if body == "" {
				body = validMCPAdmissionBody(testMCPAdmissionIdentity)
			}
			request := newMCPAdmissionRequest(body)
			if test.mutate != nil {
				test.mutate(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code)
			require.Zero(t, checks)
			require.Empty(t, response.Body.String())
		})
	}
}

func TestMCPGatewayAuthorAdmissionPerformsOneTypedIsAuthorCheck(t *testing.T) {
	tests := []struct {
		name    string
		allowed bool
		err     error
		status  int
	}{
		{name: "allow", allowed: true, status: http.StatusOK},
		{name: "deny", status: http.StatusForbidden},
		{name: "unavailable", err: errors.New("SpiceDB unavailable"), status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := 0
			handler := newTestMCPGatewayAdmissionHandler(t, gatewayAdmissionCheckerFunc(
				func(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
					checks++
					require.True(t, decision.Valid())
					require.Equal(t, testMCPAdmissionIdentity, decision.Actor().AccountIdentityID())
					require.Equal(t, policyv1.DelegationDirectSession, decision.Delegation().Kind())
					require.Equal(t, mcpGatewayAdmissionAttribution, decision.Delegation().SessionID())
					require.Equal(t, "platform", decision.Resource().Type())
					require.Equal(t, "global", decision.Resource().ID())
					require.Equal(t, "is_author", decision.Action().Name())
					require.Equal(t, "is_author", decision.Action().Permission())
					return test.allowed, test.err
				},
			))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newMCPAdmissionRequest(validMCPAdmissionBody(testMCPAdmissionIdentity)))
			require.Equal(t, test.status, response.Code)
			require.Equal(t, 1, checks)
			require.Empty(t, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		})
	}
}

func TestMCPGatewayAuthorAdmissionExactRouteAndDependencies(t *testing.T) {
	checker := gatewayAdmissionCheckerFunc(func(context.Context, policyv1.AuthorizationDecision) (bool, error) {
		return true, nil
	})
	_, err := NewMCPGatewayAuthorAdmissionHandler("", testMCPAuthHeaderName, testMCPInternalHeaderName, checker)
	require.Error(t, err)
	var typedNil *nilGatewayAdmissionChecker
	_, err = NewMCPGatewayAuthorAdmissionHandler(testMCPAdmissionSecret, testMCPAuthHeaderName, testMCPInternalHeaderName, typedNil)
	require.Error(t, err)

	handler := newTestMCPGatewayAdmissionHandler(t, checker)
	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodGet, path: MCPGatewayAuthorAdmissionPath, status: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/internal/mcp/admission", status: http.StatusNotFound},
	} {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(validMCPAdmissionBody(testMCPAdmissionIdentity)))
		request.Header.Set(testMCPInternalHeaderName, testMCPAdmissionSecret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, test.status, response.Code)
		require.Empty(t, response.Body.String())
	}
}

func newTestMCPGatewayAdmissionHandler(t *testing.T, checker GatewayAdmissionChecker) http.Handler {
	t.Helper()
	handler, err := NewMCPGatewayAuthorAdmissionHandler(testMCPAdmissionSecret, testMCPAuthHeaderName, testMCPInternalHeaderName, checker)
	require.NoError(t, err)
	return handler
}

func newMCPAdmissionRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, MCPGatewayAuthorAdmissionPath, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(testMCPInternalHeaderName, testMCPAdmissionSecret)
	return request
}

func validMCPAdmissionBody(accountIdentityID string) string {
	return `{"account_identity_id":"` + accountIdentityID + `"}`
}
