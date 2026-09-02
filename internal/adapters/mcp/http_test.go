package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"google.golang.org/protobuf/proto"
)

const (
	testInternalServiceSecret     = "test-internal-service-secret"
	testAuthHeaderName            = "X-Authenticated-Context-B64"
	testInternalServiceHeaderName = "X-Internal-Service"
	testIdentity                  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testMember                    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testSelector                  = "hydra-client-123"
	testDelegationName            = "Example Member · Example Client"
	testBearer                    = "credential-must-not-be-reflected"
)

func TestNewHTTPHandlerRequiresGatewayAndToolDependencies(t *testing.T) {
	valid := validHTTPConfig(nil)
	tests := []struct {
		name   string
		mutate func(*HTTPConfig)
	}{
		{name: "internal service secret", mutate: func(config *HTTPConfig) { config.InternalServiceSecret = "" }},
		{name: "auth header name", mutate: func(config *HTTPConfig) { config.AuthHeaderName = "X Invalid" }},
		{name: "internal service header name", mutate: func(config *HTTPConfig) { config.InternalServiceHeaderName = "Authorization" }},
		{name: "shared header name", mutate: func(config *HTTPConfig) { config.InternalServiceHeaderName = config.AuthHeaderName }},
		{name: "registry", mutate: func(config *HTTPConfig) { config.Registry = nil }},
		{name: "dispatcher", mutate: func(config *HTTPConfig) { config.Dispatcher = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewHTTPHandler(config); err == nil {
				t.Fatal("invalid production HTTP config was accepted")
			}
		})
	}
}

func TestAssembledHTTPHandlerUsesOnlyGatewayAssertionForMainRequest(t *testing.T) {
	registryCalls := 0
	config := validHTTPConfig(nil)
	config.Registry = registryFunc(func(ctx context.Context, principal mcpserver.Principal) ([]mcpserver.Tool, error) {
		registryCalls++
		user := auth.GetUser(ctx)
		if user == nil || user.IdentityID.String() != testIdentity || user.MemberID.String() != testMember ||
			principal.DelegationID != testSelector || principal.DelegationName != testDelegationName {
			t.Fatalf("authenticated request = user:%+v principal:%+v", user, principal)
		}
		return []mcpserver.Tool{}, nil
	})
	handler := newHTTPTestHandler(t, config)
	request := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || registryCalls != 1 {
		t.Fatalf("response = %d %s; registry calls=%d", response.Code, response.Body.String(), registryCalls)
	}
}

func validHTTPConfig(_ any) HTTPConfig {
	return HTTPConfig{
		InternalServiceSecret:     testInternalServiceSecret,
		AuthHeaderName:            testAuthHeaderName,
		InternalServiceHeaderName: testInternalServiceHeaderName,
		Registry: registryFunc(func(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
			return []mcpserver.Tool{{Name: "document_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		}),
		Dispatcher: dispatcherFunc(func(context.Context, mcpserver.Principal, string, mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
			return mcpserver.ToolResult{Content: []mcpserver.ContentBlock{mcpserver.TextContent("ok")}}, nil
		}),
		ServerInfo: mcpserver.Implementation{Name: "geul", Version: "test"},
	}
}

func newHTTPTestHandler(t *testing.T, config HTTPConfig) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(config)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func mcpHTTPRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	request.Header.Set(testInternalServiceHeaderName, testInternalServiceSecret)
	raw, err := proto.Marshal(&intrav1.MCPAuthenticatedContext{
		IdentityId: testIdentity, MemberId: testMember, DelegationId: testSelector,
		DelegationName:   testDelegationName,
		DelegationMethod: intrav1.MCPDelegationMethod_MCP_DELEGATION_METHOD_OAUTH,
	})
	if err != nil {
		panic(err)
	}
	request.Header.Set(testAuthHeaderName, base64.RawURLEncoding.EncodeToString(raw))
	return request
}
