package mcp

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"google.golang.org/protobuf/proto"
)

type nilHTTPHandler struct{}

func (*nilHTTPHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("typed nil HTTP handler must not be called")
}

func TestAuthenticationHTTPConstructorsRejectTypedNilDependencies(t *testing.T) {
	t.Parallel()
	var next *nilHTTPHandler
	if handler, err := NewGatewayAssertionHandler(testInternalServiceSecret, testAuthHeaderName, testInternalServiceHeaderName, next); !errors.Is(err, ErrInvalidDependency) || handler != nil {
		t.Fatalf("NewGatewayAssertionHandler(typed nil) = %+v, %v", handler, err)
	}
}

func TestGatewayAssertionHandlerRejectsMalformedContextsAndResidualCredentials(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *http.Request)
	}{
		{name: "caller spoof without secret", mutate: func(_ *testing.T, request *http.Request) { request.Header.Del(testInternalServiceHeaderName) }},
		{name: "wrong secret", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set(testInternalServiceHeaderName, "wrong") }},
		{name: "duplicate secret", mutate: func(_ *testing.T, request *http.Request) {
			request.Header.Add(testInternalServiceHeaderName, testInternalServiceSecret)
		}},
		{name: "residual Authorization", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set("Authorization", "Bearer "+testBearer) }},
		{name: "whitespace Authorization", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set("Authorization", " ") }},
		{name: "residual Cookie", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set("Cookie", "ory_kratos_session=caller") }},
		{name: "residual browser session", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set("X-Session-Id", "caller-session") }},
		{name: "missing context", mutate: func(_ *testing.T, request *http.Request) { request.Header.Del(testAuthHeaderName) }},
		{name: "duplicate context", mutate: func(_ *testing.T, request *http.Request) {
			request.Header.Add(testAuthHeaderName, request.Header.Get(testAuthHeaderName))
		}},
		{name: "malformed base64", mutate: func(_ *testing.T, request *http.Request) { request.Header.Set(testAuthHeaderName, "***") }},
		{name: "padded base64", mutate: func(_ *testing.T, request *http.Request) {
			request.Header.Set(testAuthHeaderName, request.Header.Get(testAuthHeaderName)+"=")
		}},
		{name: "oversized context", mutate: func(_ *testing.T, request *http.Request) {
			request.Header.Set(testAuthHeaderName, strings.Repeat("A", maxAuthenticatedContextEncodedBytes+4))
		}},
		{name: "missing identity", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.IdentityId = "" })},
		{name: "noncanonical identity", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.IdentityId = strings.ToUpper(testIdentity) })},
		{name: "zero identity", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) {
			context.IdentityId = "00000000-0000-0000-0000-000000000000"
		})},
		{name: "malformed member", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.MemberId = "not-uuid" })},
		{name: "blank delegation id", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.DelegationId = " " })},
		{name: "oversized delegation id", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.DelegationId = strings.Repeat("a", 2049) })},
		{name: "blank name", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.DelegationName = " " })},
		{name: "too many name runes", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.DelegationName = strings.Repeat("a", 101) })},
		{name: "unknown delegation method", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) { context.DelegationMethod = 99 })},
		{name: "unspecified delegation method", mutate: mutateContext(func(context *intrav1.MCPAuthenticatedContext) {
			context.DelegationMethod = intrav1.MCPDelegationMethod_MCP_DELEGATION_METHOD_UNSPECIFIED
		})},
		{name: "unknown protobuf field", mutate: func(t *testing.T, request *http.Request) {
			raw, err := base64.RawURLEncoding.DecodeString(request.Header.Get(testAuthHeaderName))
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, 0x30, 0x01)
			request.Header.Set(testAuthHeaderName, base64.RawURLEncoding.EncodeToString(raw))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler, err := NewGatewayAssertionHandler(testInternalServiceSecret, testAuthHeaderName, testInternalServiceHeaderName, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			if err != nil {
				t.Fatal(err)
			}
			request := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
			test.mutate(t, request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || called {
				t.Fatalf("response = %d %s; next=%v", response.Code, response.Body.String(), called)
			}
			if strings.Contains(response.Body.String(), testBearer) || strings.Contains(response.Body.String(), testInternalServiceSecret) {
				t.Fatalf("error leaked credential: %s", response.Body.String())
			}
		})
	}
}

func mutateContext(mutate func(*intrav1.MCPAuthenticatedContext)) func(*testing.T, *http.Request) {
	return func(t *testing.T, request *http.Request) {
		t.Helper()
		raw, err := base64.RawURLEncoding.DecodeString(request.Header.Get(testAuthHeaderName))
		if err != nil {
			t.Fatal(err)
		}
		var context intrav1.MCPAuthenticatedContext
		if err := proto.Unmarshal(raw, &context); err != nil {
			t.Fatal(err)
		}
		mutate(&context)
		raw, err = proto.Marshal(&context)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(testAuthHeaderName, base64.RawURLEncoding.EncodeToString(raw))
	}
}

func TestGatewayAssertionHandlerBindsPrincipalAndScrubsIngressHeaders(t *testing.T) {
	called := false
	handler, err := NewGatewayAssertionHandler(testInternalServiceSecret, testAuthHeaderName, testInternalServiceHeaderName, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		called = true
		principal, ok := mcpserver.PrincipalFromContext(request.Context())
		if !ok || principal.IdentityID != testIdentity || principal.MemberID != testMember ||
			principal.DelegationID != testSelector || principal.DelegationName != testDelegationName ||
			principal.DelegationMethod != mcpserver.DelegationMethodMCPOAuth {
			t.Fatalf("principal = %+v/%v", principal, ok)
		}
		for _, name := range []string{
			testInternalServiceHeaderName, testAuthHeaderName, "Authorization", "Cookie", "X-Session-Id",
		} {
			if len(request.Header.Values(name)) != 0 {
				t.Fatalf("ingress header %s was retained", name)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	request.Header["Authorization"] = []string{""}
	request.Header["Cookie"] = []string{""}
	request.Header["X-Session-Id"] = []string{""}
	handler.ServeHTTP(response, request)
	if !called {
		t.Fatal("valid assertion did not reach MCP handler")
	}
}

func TestDelegationMethodSpecificIDs(t *testing.T) {
	maxID := strings.Repeat("a", 2048)
	for _, test := range []struct {
		name   string
		method mcpserver.DelegationMethod
		value  string
		want   bool
	}{
		{name: "PAT method rejected", method: mcpserver.DelegationMethod("mcp_pat"), value: testSelector, want: false},
		{name: "OAuth CIMD URL", method: mcpserver.DelegationMethodMCPOAuth, value: "https://client.example/mcp/client.json", want: true},
		{name: "OAuth DCR opaque", method: mcpserver.DelegationMethodMCPOAuth, value: "hydra-client_123", want: true},
		{name: "OAuth ID maximum", method: mcpserver.DelegationMethodMCPOAuth, value: maxID, want: true},
		{name: "OAuth ID too large", method: mcpserver.DelegationMethodMCPOAuth, value: maxID + "a", want: false},
		{name: "CIMD root URL", method: mcpserver.DelegationMethodMCPOAuth, value: "https://client.example/", want: false},
		{name: "CIMD HTTP URL", method: mcpserver.DelegationMethodMCPOAuth, value: "http://client.example/mcp.json", want: false},
		{name: "DCR whitespace", method: mcpserver.DelegationMethodMCPOAuth, value: "client id", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validDelegationID(test.method, test.value); got != test.want {
				t.Fatalf("delegation ID %q accepted=%v, want %v", test.value, got, test.want)
			}
		})
	}
}
