package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

const testToken = "pat_test_super_secret"

type registryFunc func(context.Context, Principal) ([]Tool, error)

func (function registryFunc) ListTools(ctx context.Context, principal Principal) ([]Tool, error) {
	return function(ctx, principal)
}

type dispatcherFunc func(context.Context, Principal, string, ToolArguments) (ToolResult, error)

func (function dispatcherFunc) CallTool(ctx context.Context, principal Principal, name string, arguments ToolArguments) (ToolResult, error) {
	return function(ctx, principal, name, arguments)
}

type serverTitleSourceFunc func(context.Context) (string, error)

func (function serverTitleSourceFunc) ServerTitle(ctx context.Context) (string, error) {
	return function(ctx)
}

func TestTransportMethodContentTypeAndAccept(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})

	tests := []struct {
		name        string
		method      string
		contentType string
		accept      string
		wantStatus  int
	}{
		{name: "unsupported method", method: http.MethodPut, wantStatus: http.StatusMethodNotAllowed},
		{name: "post missing content type", method: http.MethodPost, accept: requiredAccept, wantStatus: http.StatusUnsupportedMediaType},
		{name: "post wrong content type", method: http.MethodPost, contentType: "text/plain", accept: requiredAccept, wantStatus: http.StatusUnsupportedMediaType},
		{name: "post rejects non-UTF-8 charset", method: http.MethodPost, contentType: "application/json; charset=iso-8859-1", accept: requiredAccept, wantStatus: http.StatusUnsupportedMediaType},
		{name: "post accepts charset", method: http.MethodPost, contentType: "application/json; charset=utf-8", accept: requiredAccept, wantStatus: http.StatusOK},
		{name: "post missing sse accept", method: http.MethodPost, contentType: "application/json", accept: "application/json", wantStatus: http.StatusNotAcceptable},
		{name: "post sse disabled by quality", method: http.MethodPost, contentType: "application/json", accept: "application/json, text/event-stream;q=0", wantStatus: http.StatusNotAcceptable},
		{name: "get without sse accept", method: http.MethodGet, accept: "application/json", wantStatus: http.StatusNotAcceptable},
		{name: "get valid accept has no stream", method: http.MethodGet, accept: "text/event-stream", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := ""
			if test.method == http.MethodPost {
				body = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
			}
			request := httptest.NewRequest(test.method, "/mcp", strings.NewReader(body))
			request = request.WithContext(mustPrincipalContext(t, request.Context(), validPrincipal()))
			if test.method == http.MethodGet {
				request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.accept != "" {
				request.Header.Set("Accept", test.accept)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestOriginValidation(t *testing.T) {
	handler := newTestHandler(t, testDependencies{
		allowedOrigins: []string{"https://editor.geul.example"},
	})

	tests := []struct {
		name       string
		origin     string
		wantStatus int
	}{
		{name: "no browser origin", wantStatus: http.StatusOK},
		{name: "allowed origin", origin: "HTTPS://EDITOR.GEUL.EXAMPLE/", wantStatus: http.StatusOK},
		{name: "unlisted origin", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
		{name: "opaque origin", origin: "null", wantStatus: http.StatusForbidden},
		{name: "origin with path", origin: "https://editor.geul.example/path", wantStatus: http.StatusForbidden},
		{name: "multiple origins", origin: "https://editor.geul.example", wantStatus: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.name == "multiple origins" {
				request.Header.Add("Origin", "https://attacker.example")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestHandlerRequiresAssertedPrincipalAndRejectsResidualAuthorization(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	missing := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	missing = missing.WithContext(context.Background())
	residual := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	residual.Header.Set("Authorization", "Bearer "+testToken)
	emptyResidual := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	emptyResidual.Header["Authorization"] = []string{""}
	whitespaceResidual := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	whitespaceResidual.Header.Set("Authorization", " ")
	valid := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	for name, test := range map[string]struct {
		request *http.Request
		status  int
	}{
		"missing principal":       {request: missing, status: http.StatusUnauthorized},
		"residual bearer":         {request: residual, status: http.StatusUnauthorized},
		"empty scrubbed residual": {request: emptyResidual, status: http.StatusOK},
		"whitespace residual":     {request: whitespaceResidual, status: http.StatusUnauthorized},
		"asserted principal":      {request: valid, status: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != test.status || strings.Contains(response.Body.String(), testToken) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("core handler advertised bearer auth: %v", response.Header())
			}
		})
	}
}

func TestInitializeAcceptsPublishedVersionAndOnlyToolsCapability(t *testing.T) {
	handler := newTestHandler(t, testDependencies{instructions: "Use document_list before document_open."})
	request := rpcRequest(`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"roots":{}},"clientInfo":{"name":"test-client","version":"2"}}}`)
	request.Header.Del("MCP-Protocol-Version")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		ID     string `json:"id"`
		Result struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
			ServerInfo      Implementation             `json:"serverInfo"`
			Instructions    string                     `json:"instructions"`
		} `json:"result"`
	}
	decodeResponse(t, response, &envelope)
	if envelope.ID != "init-1" {
		t.Fatalf("id = %q", envelope.ID)
	}
	if envelope.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocolVersion = %q", envelope.Result.ProtocolVersion)
	}
	if keys := mapKeys(envelope.Result.Capabilities); !slices.Equal(keys, []string{"tools"}) {
		t.Fatalf("capabilities = %v, want only tools", keys)
	}
	var toolsCapability struct {
		ListChanged bool `json:"listChanged"`
	}
	if err := json.Unmarshal(envelope.Result.Capabilities["tools"], &toolsCapability); err != nil {
		t.Fatal(err)
	}
	if toolsCapability.ListChanged {
		t.Fatal("static registry advertised list change notifications")
	}
	if envelope.Result.ServerInfo.Name != "geul" || envelope.Result.ServerInfo.Version != "test" {
		t.Fatalf("server info = %+v", envelope.Result.ServerInfo)
	}
	if envelope.Result.Instructions != "Use document_list before document_open." {
		t.Fatalf("instructions = %q", envelope.Result.Instructions)
	}
}

func TestInitializeUsesCurrentHumanFacingServerTitle(t *testing.T) {
	title := "Geul"
	handler := newTestHandler(t, testDependencies{
		serverTitleSource: serverTitleSourceFunc(func(context.Context) (string, error) { return title, nil }),
	})

	initialize := func() Implementation {
		request := rpcRequest(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`)
		request.Header.Del("MCP-Protocol-Version")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var envelope struct {
			Result struct {
				ServerInfo Implementation `json:"serverInfo"`
			} `json:"result"`
		}
		decodeResponse(t, response, &envelope)
		return envelope.Result.ServerInfo
	}

	requireFirst := initialize()
	if requireFirst.Name != "geul" || requireFirst.Title != "Geul" {
		t.Fatalf("server info = %+v", requireFirst)
	}
	title = "Second Site Title"
	requireSecond := initialize()
	if requireSecond.Name != "geul" || requireSecond.Title != "Second Site Title" {
		t.Fatalf("server info = %+v", requireSecond)
	}
}

func TestInitializeFailsClosedWhenServerTitleCannotBeRead(t *testing.T) {
	handler := newTestHandler(t, testDependencies{
		serverTitleSource: serverTitleSourceFunc(func(context.Context) (string, error) {
			return "", errors.New("site settings unavailable")
		}),
	})
	request := rpcRequest(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`)
	request.Header.Del("MCP-Protocol-Version")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":-32603`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestInitializeRejectsUnsupportedOrMissingVersion(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	tests := []struct {
		name        string
		params      string
		wantMessage string
	}{
		{
			name:        "unsupported published revision",
			params:      `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`,
			wantMessage: "Unsupported protocol version",
		},
		{
			name:        "empty version",
			params:      `{"protocolVersion":"","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`,
			wantMessage: "Invalid params",
		},
		{
			name:        "missing version",
			params:      `{"capabilities":{},"clientInfo":{"name":"test","version":"1"}}`,
			wantMessage: "Invalid params",
		},
		{
			name:        "non-string version",
			params:      `{"protocolVersion":20251125,"capabilities":{},"clientInfo":{"name":"test","version":"1"}}`,
			wantMessage: "Invalid params",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := rpcRequest(`{"jsonrpc":"2.0","id":"init-rejected","method":"initialize","params":` + test.params + `}`)
			request.Header.Del("MCP-Protocol-Version")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
			}
			var envelope struct {
				ID     string          `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			decodeResponse(t, response, &envelope)
			if envelope.ID != "init-rejected" || envelope.Error.Code != -32602 || envelope.Error.Message != test.wantMessage {
				t.Fatalf("response envelope = %+v", envelope)
			}
			if len(envelope.Result) != 0 {
				t.Fatalf("rejected initialize returned negotiated result: %s", envelope.Result)
			}
		})
	}
}

func TestInitializeClientInfoIsProtocolMetadataOnly(t *testing.T) {
	registryCalled := false
	handler := newTestHandler(t, testDependencies{
		registry: registryFunc(func(_ context.Context, principal Principal) ([]Tool, error) {
			registryCalled = true
			if principal.DelegationID != "hydra-client-1" || principal.DelegationName != "Example Client" {
				t.Fatalf("registry attribution = %+v", principal)
			}
			return []Tool{validTestTool("document_read", `{"type":"object"}`)}, nil
		}),
	})
	initialize := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"spoofed-credential-name","version":"1"}}}`)
	initialize.Header.Del("MCP-Protocol-Version")
	initializeResponse := httptest.NewRecorder()
	handler.ServeHTTP(initializeResponse, initialize)
	if initializeResponse.Code != http.StatusOK {
		t.Fatalf("initialize response = %d %s", initializeResponse.Code, initializeResponse.Body.String())
	}

	listResponse := serveRPC(handler, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if listResponse.Code != http.StatusOK || !registryCalled {
		t.Fatalf("list response = %d %s; registryCalled=%v", listResponse.Code, listResponse.Body.String(), registryCalled)
	}
}

func TestMissingGatewayPrincipalDoesNotAdvertiseOAuthDiscovery(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	request := rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	request = request.WithContext(context.Background())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	challenge := response.Header().Get("WWW-Authenticate")
	if challenge != "" {
		t.Fatalf("core handler emitted auth challenge %q", challenge)
	}
	if strings.Contains(strings.ToLower(challenge), "resource_metadata") || response.Header().Get("Link") != "" {
		t.Fatalf("handler advertised OAuth discovery: headers=%v", response.Header())
	}
}

func TestProtocolVersionHeaderOnSubsequentRequests(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "missing assumes unsupported legacy", wantStatus: http.StatusBadRequest},
		{name: "unsupported", header: "2025-06-18", wantStatus: http.StatusBadRequest},
		{name: "published version", header: ProtocolVersion, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := rpcRequest(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
			request.Header.Del("MCP-Protocol-Version")
			if test.header != "" {
				request.Header.Set("MCP-Protocol-Version", test.header)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if !strings.Contains(response.Body.String(), `"id":7`) {
				t.Fatalf("response did not preserve request ID: %s", response.Body.String())
			}
		})
	}
}

func TestInitializedNotificationAndPing(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})

	notification := rpcRequest(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{"_meta":{"client":"test"}}}`)
	notificationResponse := httptest.NewRecorder()
	handler.ServeHTTP(notificationResponse, notification)
	if notificationResponse.Code != http.StatusAccepted || notificationResponse.Body.Len() != 0 {
		t.Fatalf("initialized response = %d %q", notificationResponse.Code, notificationResponse.Body.String())
	}

	ping := rpcRequest(`{"jsonrpc":"2.0","id":9,"method":"ping","params":{}}`)
	pingResponse := httptest.NewRecorder()
	handler.ServeHTTP(pingResponse, ping)
	if pingResponse.Code != http.StatusOK || !strings.Contains(pingResponse.Body.String(), `"result":{}`) {
		t.Fatalf("ping response = %d %s", pingResponse.Code, pingResponse.Body.String())
	}
}

func TestRequestAndNotificationMessageKinds(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "request-only method sent as notification", body: `{"jsonrpc":"2.0","method":"tools/list"}`, wantStatus: http.StatusBadRequest},
		{name: "notification-only method sent as request", body: `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveRPC(handler, test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":-32600`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestToolsListIsDeterministicAndDoesNotMutateRegistry(t *testing.T) {
	registered := []Tool{
		validTestTool("zeta", `{"type":"object"}`),
		validTestTool("alpha", `{"type":"object","additionalProperties":false}`),
	}
	registered[0].Description = "last"
	registered[1].Description = "first"
	handler := newTestHandler(t, testDependencies{
		registry: registryFunc(func(_ context.Context, principal Principal) ([]Tool, error) {
			if principal.MemberID != validPrincipal().MemberID {
				t.Fatalf("registry principal = %+v", principal)
			}
			return registered, nil
		}),
	})
	response := serveRPC(handler, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	decodeResponse(t, response, &envelope)
	if names := []string{envelope.Result.Tools[0].Name, envelope.Result.Tools[1].Name}; !slices.Equal(names, []string{"alpha", "zeta"}) {
		t.Fatalf("tool order = %v", names)
	}
	if registered[0].Name != "zeta" {
		t.Fatal("handler mutated registry-owned tool slice")
	}
}

func TestToolsListRejectsUnissuedCursor(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	response := serveRPC(handler, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"never-issued"}}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":-32602`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolsCallDispatchesParsedArgumentsAndPrincipal(t *testing.T) {
	called := false
	verifiedPrincipal := Principal{
		IdentityID:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		MemberID:         "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb",
		DelegationID:     "https://client.example/mcp.json",
		DelegationName:   "Example Client Custom",
		DelegationMethod: DelegationMethodMCPOAuth,
	}
	handler := newTestHandler(t, testDependencies{
		registry: registryFunc(func(_ context.Context, principal Principal) ([]Tool, error) {
			if principal.DelegationID != verifiedPrincipal.DelegationID || principal.DelegationName != verifiedPrincipal.DelegationName {
				t.Fatalf("registry changed credential attribution: %+v", principal)
			}
			return []Tool{validTestTool("document_read", `{"type":"object"}`)}, nil
		}),
		dispatcher: dispatcherFunc(func(_ context.Context, principal Principal, name string, arguments ToolArguments) (ToolResult, error) {
			called = true
			if principal.MemberID != verifiedPrincipal.MemberID || principal.DelegationID != verifiedPrincipal.DelegationID ||
				principal.DelegationName != verifiedPrincipal.DelegationName || name != "document_read" {
				t.Fatalf("dispatch = principal=%+v name=%q", principal, name)
			}
			var document string
			if err := json.Unmarshal(arguments["document"], &document); err != nil || document != "post:1" {
				t.Fatalf("arguments = %s", arguments["document"])
			}
			return ToolResult{
				Content:           []ContentBlock{TextContent("read complete")},
				StructuredContent: map[string]any{"revision": "r2"},
			}, nil
		}),
	})
	request := rpcRequest(`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"document_read","arguments":{"document":"post:1"}}}`)
	request = request.WithContext(mustPrincipalContext(t, request.Context(), verifiedPrincipal))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if !called {
		t.Fatal("dispatcher was not called")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"structuredContent":{"revision":"r2"}`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolsCallProtocolAndExecutionErrors(t *testing.T) {
	dispatchCalls := 0
	tests := []struct {
		name          string
		body          string
		dispatchError error
		wantCode      int
		wantIsError   bool
		wantText      string
	}{
		{name: "unknown tool", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing","arguments":{}}}`, wantCode: -32602},
		{name: "arguments must be object", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":[]}}`, wantCode: -32602},
		{name: "explicit null arguments rejected", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":null}}`, wantCode: -32602},
		{name: "undeclared task augmentation rejected", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":{},"task":{"ttl":1000}}}`, wantCode: -32601},
		{name: "null task augmentation rejected", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":{},"task":null}}`, wantCode: -32601},
		{name: "domain validation error", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`, dispatchError: &ToolExecutionError{Message: "document reference is invalid"}, wantIsError: true, wantText: "document reference is invalid"},
		{name: "typed nil domain error fails closed", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`, dispatchError: (*ToolExecutionError)(nil), wantCode: -32603, wantText: "Internal error"},
		{name: "internal error is generic", body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`, dispatchError: errors.New("provider included " + testToken), wantCode: -32603, wantText: "Internal error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t, testDependencies{
				dispatcher: dispatcherFunc(func(_ context.Context, _ Principal, _ string, _ ToolArguments) (ToolResult, error) {
					dispatchCalls++
					return ToolResult{}, test.dispatchError
				}),
			})
			before := dispatchCalls
			response := serveRPC(handler, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
			}
			if test.wantCode != 0 && !strings.Contains(response.Body.String(), `"code":`+jsonNumber(test.wantCode)) {
				t.Fatalf("body = %s, want code %d", response.Body.String(), test.wantCode)
			}
			if test.wantIsError && !strings.Contains(response.Body.String(), `"isError":true`) {
				t.Fatalf("body = %s, want tool execution error", response.Body.String())
			}
			if test.wantText != "" && !strings.Contains(response.Body.String(), test.wantText) {
				t.Fatalf("body = %s, want %q", response.Body.String(), test.wantText)
			}
			if strings.Contains(response.Body.String(), testToken) {
				t.Fatalf("response exposed secret: %s", response.Body.String())
			}
			protocolFailure := test.name == "unknown tool" || strings.Contains(test.name, "arguments") || strings.Contains(test.name, "task augmentation")
			if protocolFailure && dispatchCalls != before {
				t.Fatal("dispatcher called for protocol error")
			}
		})
	}
}

func TestUnencodableToolResultIsInternalError(t *testing.T) {
	handler := newTestHandler(t, testDependencies{
		dispatcher: dispatcherFunc(func(context.Context, Principal, string, ToolArguments) (ToolResult, error) {
			return ToolResult{
				Content:           []ContentBlock{TextContent("not returned")},
				StructuredContent: map[string]any{"invalid": make(chan struct{})},
			}, nil
		}),
	})
	response := serveRPC(handler, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":-32603`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestJSONRPCValidationPreservesValidIDs(t *testing.T) {
	handler := newTestHandler(t, testDependencies{})
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   int
		wantID     string
	}{
		{name: "parse error", body: `{`, wantStatus: http.StatusBadRequest, wantCode: -32700},
		{name: "batch unsupported", body: `[]`, wantStatus: http.StatusBadRequest, wantCode: -32600},
		{name: "invalid JSON-RPC version", body: `{"jsonrpc":"1.0","id":"kept","method":"ping"}`, wantStatus: http.StatusBadRequest, wantCode: -32600, wantID: `"kept"`},
		{name: "null ID invalid", body: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, wantStatus: http.StatusBadRequest, wantCode: -32600},
		{name: "object ID invalid", body: `{"jsonrpc":"2.0","id":{},"method":"ping"}`, wantStatus: http.StatusBadRequest, wantCode: -32600},
		{name: "params must be object", body: `{"jsonrpc":"2.0","id":44,"method":"ping","params":[]}`, wantStatus: http.StatusOK, wantCode: -32602, wantID: `44`},
		{name: "unknown method", body: `{"jsonrpc":"2.0","id":"unknown-1","method":"unknown"}`, wantStatus: http.StatusOK, wantCode: -32601, wantID: `"unknown-1"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveRPC(handler, test.body)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"code":`+jsonNumber(test.wantCode)) {
				t.Fatalf("body = %s, want code %d", response.Body.String(), test.wantCode)
			}
			if test.wantID != "" && !strings.Contains(response.Body.String(), `"id":`+test.wantID) {
				t.Fatalf("body = %s, want id %s", response.Body.String(), test.wantID)
			}
		})
	}
}

func TestRequestBodyLimit(t *testing.T) {
	handler := newTestHandler(t, testDependencies{maxBodyBytes: 32})
	response := serveRPC(handler, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestInvalidRegistryDefinitionIsInternalError(t *testing.T) {
	handler := newTestHandler(t, testDependencies{
		registry: registryFunc(func(context.Context, Principal) ([]Tool, error) {
			return []Tool{{Name: "invalid tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		}),
	})
	response := serveRPC(handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":-32603`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestValidateToolRequiresTopLevelObjectSchemas(t *testing.T) {
	tests := []struct {
		name            string
		inputSchema     string
		outputSchema    string
		securitySchemes []ToolSecurityScheme
		metaSchemes     []ToolSecurityScheme
		omitSecurity    bool
		omitMetaMirror  bool
		annotations     map[string]any
		wantError       bool
	}{
		{
			name:            "object schemas with extensions",
			inputSchema:     `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"document":{"type":"string"}},"x-geul":"allowed"}`,
			outputSchema:    `{"type":"object","additionalProperties":false}`,
			securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}},
		},
		{name: "noauth is outside the Geul contract", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "noauth"}}, wantError: true},
		{name: "output schema may be absent", inputSchema: `{"type":"object"}`},
		{name: "empty input schema", inputSchema: `{}`, wantError: true},
		{name: "missing input type", inputSchema: `{"properties":{}}`, wantError: true},
		{name: "wrong input type", inputSchema: `{"type":"array"}`, wantError: true},
		{name: "non-string input type", inputSchema: `{"type":["object"]}`, wantError: true},
		{name: "empty output schema", inputSchema: `{"type":"object"}`, outputSchema: `{}`, wantError: true},
		{name: "wrong output type", inputSchema: `{"type":"object"}`, outputSchema: `{"type":"string"}`, wantError: true},
		{name: "non-string output type", inputSchema: `{"type":"object"}`, outputSchema: `{"type":null}`, wantError: true},
		{name: "unsupported security scheme", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "apiKey"}}, wantError: true},
		{name: "noauth with scopes", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "noauth", Scopes: []string{"mcp"}}}, wantError: true},
		{name: "oauth without scopes", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2"}}, wantError: true},
		{name: "oauth with blank scope", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{" "}}}, wantError: true},
		{name: "oauth with a different scope", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"other"}}}, wantError: true},
		{name: "oauth without offline access", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp"}}}, wantError: true},
		{name: "oauth with reversed scopes", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"offline_access", "mcp"}}}, wantError: true},
		{name: "oauth with duplicate scope", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "mcp"}}}, wantError: true},
		{name: "multiple schemes are outside the Geul contract", inputSchema: `{"type":"object"}`, securitySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}, {Type: "noauth"}}, wantError: true},
		{name: "missing security schemes", inputSchema: `{"type":"object"}`, omitSecurity: true, wantError: true},
		{name: "missing security mirror", inputSchema: `{"type":"object"}`, omitMetaMirror: true, wantError: true},
		{name: "mismatched security mirror", inputSchema: `{"type":"object"}`, metaSchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"other"}}}, wantError: true},
		{name: "missing annotations", inputSchema: `{"type":"object"}`, annotations: map[string]any{}, wantError: true},
		{name: "non-boolean annotation", inputSchema: `{"type":"object"}`, annotations: map[string]any{"readOnlyHint": "true", "destructiveHint": false, "openWorldHint": false}, wantError: true},
		{name: "contradictory read-only annotation", inputSchema: `{"type":"object"}`, annotations: map[string]any{"readOnlyHint": true, "destructiveHint": true, "openWorldHint": false}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			securitySchemes := test.securitySchemes
			if securitySchemes == nil && !test.omitSecurity {
				securitySchemes = []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}}
			}
			annotations := test.annotations
			if annotations == nil {
				annotations = map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
			}
			tool := Tool{
				Name:            "document_read",
				InputSchema:     json.RawMessage(test.inputSchema),
				SecuritySchemes: securitySchemes,
				Annotations:     annotations,
			}
			if !test.omitMetaMirror && !test.omitSecurity {
				metaSchemes := test.metaSchemes
				if metaSchemes == nil {
					metaSchemes = securitySchemes
				}
				tool.Meta = map[string]any{"securitySchemes": metaSchemes}
			}
			if test.outputSchema != "" {
				tool.OutputSchema = json.RawMessage(test.outputSchema)
			}
			err := validateTool(tool)
			if (err != nil) != test.wantError {
				t.Fatalf("validateTool() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

type testDependencies struct {
	registry          ToolRegistry
	dispatcher        ToolDispatcher
	instructions      string
	allowedOrigins    []string
	maxBodyBytes      int64
	serverTitleSource ServerTitleSource
}

const requiredAccept = "application/json, text/event-stream"

func newTestHandler(t *testing.T, dependencies testDependencies) http.Handler {
	t.Helper()
	if dependencies.registry == nil {
		dependencies.registry = registryFunc(func(context.Context, Principal) ([]Tool, error) {
			return []Tool{{
				Name:            "document_read",
				Description:     "Read a Geul document",
				InputSchema:     json.RawMessage(`{"type":"object"}`),
				OutputSchema:    json.RawMessage(`{"type":"object"}`),
				SecuritySchemes: []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}},
				Annotations:     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
				Meta: map[string]any{
					"securitySchemes": []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}},
				},
			}}, nil
		})
	}
	if dependencies.dispatcher == nil {
		dependencies.dispatcher = dispatcherFunc(func(context.Context, Principal, string, ToolArguments) (ToolResult, error) {
			return ToolResult{Content: []ContentBlock{TextContent("ok")}}, nil
		})
	}
	handler, err := NewHandler(Config{
		Registry:          dependencies.registry,
		Dispatcher:        dependencies.dispatcher,
		ServerInfo:        Implementation{Name: "geul", Version: "test"},
		ServerTitleSource: dependencies.serverTitleSource,
		Instructions:      dependencies.instructions,
		AllowedOrigins:    dependencies.allowedOrigins,
		MaxBodyBytes:      dependencies.maxBodyBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func validTestTool(name, inputSchema string) Tool {
	securitySchemes := []ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}}
	return Tool{
		Name:            name,
		InputSchema:     json.RawMessage(inputSchema),
		SecuritySchemes: securitySchemes,
		Annotations:     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
		Meta:            map[string]any{"securitySchemes": securitySchemes},
	}
}

func validPrincipal() Principal {
	return Principal{
		IdentityID:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		MemberID:         "bbbbbbbb-bbbb-4bbb-9bbb-bbbbbbbbbbbb",
		DelegationID:     "hydra-client-1",
		DelegationName:   "Example Client",
		DelegationMethod: DelegationMethodMCPOAuth,
	}
}

func rpcRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", requiredAccept)
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	request = request.WithContext(mustPrincipalContext(nil, request.Context(), validPrincipal()))
	return request
}

func mustPrincipalContext(t *testing.T, ctx context.Context, principal Principal) context.Context {
	authenticated, err := WithPrincipal(ctx, principal)
	if err != nil {
		if t != nil {
			t.Helper()
			t.Fatal(err)
		}
		panic(err)
	}
	return authenticated
}

func serveRPC(handler http.Handler, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, rpcRequest(body))
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
