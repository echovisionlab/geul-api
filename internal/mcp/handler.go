package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the published MCP revision implemented by Handler.
	ProtocolVersion = "2025-11-25"

	DelegationMethodMCPOAuth DelegationMethod = "mcp_oauth"

	defaultMaxBodyBytes int64 = 1 << 20
)

var (
	// ErrUnknownTool may be returned by a dispatcher if its registry changed
	// between discovery and dispatch.
	ErrUnknownTool = errors.New("mcp: unknown tool")
)

// DelegationMethod records the verified MCP authentication channel for
// attribution. It never selects a permission or changes authorization.
type DelegationMethod string

// Principal is the active account identity, Member, and delegation attribution
// asserted by the trusted HTTP ingress. DelegationID is the OAuth client ID and
// DelegationName is its user-facing attribution. The raw bearer is never part
// of Principal or this package.
type Principal struct {
	IdentityID       string
	MemberID         string
	DelegationID     string
	DelegationName   string
	DelegationMethod DelegationMethod
}

// Implementation is protocol metadata exchanged during initialization. Client
// values are never authentication or attribution authority.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// ServerTitleSource resolves the current human-facing site title for MCP
// initialization. The stable protocol identifier remains Implementation.Name.
type ServerTitleSource interface {
	ServerTitle(ctx context.Context) (string, error)
}

// Tool combines the 2025-11-25 MCP tool definition with OpenAI's documented
// tool-level securitySchemes extension and its _meta compatibility mirror.
// InputSchema and OutputSchema must be JSON objects. The handler validates
// definitions before returning them and orders them by name.
type Tool struct {
	Name            string               `json:"name"`
	Title           string               `json:"title,omitempty"`
	Description     string               `json:"description,omitempty"`
	InputSchema     json.RawMessage      `json:"inputSchema"`
	OutputSchema    json.RawMessage      `json:"outputSchema,omitempty"`
	SecuritySchemes []ToolSecurityScheme `json:"securitySchemes,omitempty"`
	Annotations     map[string]any       `json:"annotations,omitempty"`
	Meta            map[string]any       `json:"_meta,omitempty"`
}

// ToolSecurityScheme is the OpenAI tool-level authentication declaration.
// Geul tools request the resource scope and the authorization-server scope
// that permits a rotating refresh token. Resource authorization still
// requires only mcp.
type ToolSecurityScheme struct {
	Type   string   `json:"type"`
	Scopes []string `json:"scopes,omitempty"`
}

// ToolArguments contains only the parsed tools/call arguments object, never
// the bearer token or complete HTTP request body.
type ToolArguments map[string]json.RawMessage

// ContentBlock is one MCP tool-result content object. Domain adapters are
// responsible for constructing a content type allowed by MCP 2025-11-25.
type ContentBlock map[string]any

// TextContent creates a text tool-result content block.
func TextContent(text string) ContentBlock {
	return ContentBlock{"type": "text", "text": text}
}

// ToolResult is the MCP result returned by a domain dispatcher.
type ToolResult struct {
	Content           []ContentBlock `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// ToolRegistry discovers the tools visible to a verified Member. The handler
// validates and deterministically sorts each returned snapshot.
type ToolRegistry interface {
	ListTools(ctx context.Context, principal Principal) ([]Tool, error)
}

// ToolDispatcher invokes an owning-domain tool without duplicating domain
// validation, authorization, lifecycle, CAS, or side effects in this package.
type ToolDispatcher interface {
	CallTool(ctx context.Context, principal Principal, name string, arguments ToolArguments) (ToolResult, error)
}

// ToolExecutionError is safe, actionable domain feedback that is returned as a
// successful JSON-RPC tools/call result with isError=true. Other dispatcher
// errors become a generic JSON-RPC internal error and are never returned raw.
type ToolExecutionError struct {
	Message string
}

func (e *ToolExecutionError) Error() string {
	return e.Message
}

// Config supplies the complete transport boundary. AllowedOrigins is an exact
// allowlist of http(s) origins; when empty, requests without Origin remain
// valid and every presented Origin is rejected.
type Config struct {
	Registry          ToolRegistry
	Dispatcher        ToolDispatcher
	ServerInfo        Implementation
	ServerTitleSource ServerTitleSource
	Instructions      string
	AllowedOrigins    []string
	MaxBodyBytes      int64
}

// NewHandler constructs a stateless MCP 2025-11-25 Streamable HTTP handler.
func NewHandler(config Config) (http.Handler, error) {
	if config.Registry == nil {
		return nil, errors.New("mcp: tool registry is required")
	}
	if config.Dispatcher == nil {
		return nil, errors.New("mcp: tool dispatcher is required")
	}
	if config.ServerInfo.Name == "" || config.ServerInfo.Version == "" {
		return nil, errors.New("mcp: server name and version are required")
	}
	if strings.TrimSpace(config.ServerInfo.Title) != config.ServerInfo.Title {
		return nil, errors.New("mcp: server title must be canonical")
	}
	if strings.TrimSpace(config.Instructions) != config.Instructions || len(config.Instructions) > 4096 {
		return nil, errors.New("mcp: server instructions must be canonical and at most 4096 bytes")
	}

	allowedOrigins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		canonical, ok := canonicalOrigin(origin)
		if !ok {
			return nil, fmt.Errorf("mcp: invalid allowed origin %q", origin)
		}
		allowedOrigins[canonical] = struct{}{}
	}

	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	if maxBodyBytes < 0 {
		return nil, errors.New("mcp: maximum body size must be positive")
	}

	return &handler{
		registry:          config.Registry,
		dispatcher:        config.Dispatcher,
		serverInfo:        config.ServerInfo,
		serverTitleSource: config.ServerTitleSource,
		instructions:      config.Instructions,
		allowedOrigins:    allowedOrigins,
		maxBodyBytes:      maxBodyBytes,
	}, nil
}

type handler struct {
	registry          ToolRegistry
	dispatcher        ToolDispatcher
	serverInfo        Implementation
	serverTitleSource ServerTitleSource
	instructions      string
	allowedOrigins    map[string]struct{}
	maxBodyBytes      int64
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !h.originAllowed(request.Header.Values("Origin")) {
		writeHTTPError(response, http.StatusForbidden, "Forbidden")
		return
	}

	switch request.Method {
	case http.MethodGet:
		h.serveGET(response, request)
	case http.MethodPost:
		h.servePOST(response, request)
	default:
		response.Header().Set("Allow", http.MethodPost)
		writeHTTPError(response, http.StatusMethodNotAllowed, "Method Not Allowed")
	}
}

func (h *handler) serveGET(response http.ResponseWriter, request *http.Request) {
	if _, ok := h.authenticate(response, request); !ok {
		return
	}
	if !accepts(request.Header.Values("Accept"), "text/event-stream") {
		writeHTTPError(response, http.StatusNotAcceptable, "Not Acceptable")
		return
	}
	if !h.protocolVersionAllowed(request, "") {
		writeRPCError(response, http.StatusBadRequest, nil, -32600, "Unsupported MCP protocol version")
		return
	}
	response.Header().Set("Allow", http.MethodPost)
	writeHTTPError(response, http.StatusMethodNotAllowed, "Method Not Allowed")
}

func (h *handler) servePOST(response http.ResponseWriter, request *http.Request) {
	if !isJSONContentType(request.Header.Values("Content-Type")) {
		writeHTTPError(response, http.StatusUnsupportedMediaType, "Unsupported Media Type")
		return
	}
	acceptValues := request.Header.Values("Accept")
	if !accepts(acceptValues, "application/json") || !accepts(acceptValues, "text/event-stream") {
		writeHTTPError(response, http.StatusNotAcceptable, "Not Acceptable")
		return
	}

	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, h.maxBodyBytes+1))
	if err != nil {
		writeHTTPError(response, http.StatusBadRequest, "Bad Request")
		return
	}
	if int64(len(body)) > h.maxBodyBytes {
		writeHTTPError(response, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
		return
	}

	message, protocolErr := parseMessage(body)
	if protocolErr != nil {
		writeRPCError(response, protocolErr.status, protocolErr.id, protocolErr.code, protocolErr.message)
		return
	}

	if !h.protocolVersionAllowed(request, message.method) {
		writeRPCError(response, http.StatusBadRequest, message.responseID(), -32600, "Unsupported MCP protocol version")
		return
	}

	if !message.hasID {
		h.handleNotification(response, message)
		return
	}

	h.handleRequest(response, request, principal, message)
}

func (h *handler) handleNotification(response http.ResponseWriter, message incomingMessage) {
	switch message.method {
	case "initialize", "ping", "tools/list", "tools/call":
		writeRPCError(response, http.StatusBadRequest, nil, -32600, "Invalid Request")
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

func (h *handler) handleRequest(response http.ResponseWriter, request *http.Request, principal Principal, message incomingMessage) {
	if strings.HasPrefix(message.method, "notifications/") {
		writeRPCError(response, http.StatusOK, message.id, -32600, "Invalid Request")
		return
	}
	switch message.method {
	case "initialize":
		h.handleInitialize(request.Context(), response, message)
	case "ping":
		if !paramsAreObjectOrAbsent(message.params) {
			writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
			return
		}
		writeRPCResult(response, message.id, map[string]any{})
	case "tools/list":
		h.handleToolsList(response, request, principal, message)
	case "tools/call":
		h.handleToolsCall(response, request, principal, message)
	default:
		writeRPCError(response, http.StatusOK, message.id, -32601, "Method not found")
	}
}

func (h *handler) handleInitialize(ctx context.Context, response http.ResponseWriter, message incomingMessage) {
	var params struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      Implementation  `json:"clientInfo"`
	}
	if err := json.Unmarshal(message.params, &params); err != nil || params.ProtocolVersion == "" ||
		params.ClientInfo.Name == "" || params.ClientInfo.Version == "" || !isJSONObject(params.Capabilities) {
		writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
		return
	}
	if params.ProtocolVersion != ProtocolVersion {
		writeRPCError(response, http.StatusOK, message.id, -32602, "Unsupported protocol version")
		return
	}

	serverInfo := h.serverInfo
	if h.serverTitleSource != nil {
		title, err := h.serverTitleSource.ServerTitle(ctx)
		if err != nil {
			writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
			return
		}
		serverInfo.Title = strings.TrimSpace(title)
	}

	writeRPCResult(response, message.id, struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      Implementation `json:"serverInfo"`
		Instructions    string         `json:"instructions,omitempty"`
	}{
		ProtocolVersion: ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		ServerInfo:   serverInfo,
		Instructions: h.instructions,
	})
}

func (h *handler) handleToolsList(response http.ResponseWriter, request *http.Request, principal Principal, message incomingMessage) {
	if len(message.params) != 0 {
		var params map[string]json.RawMessage
		if err := json.Unmarshal(message.params, &params); err != nil {
			writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
			return
		}
		if _, present := params["cursor"]; present {
			writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
			return
		}
	}

	tools, err := h.listTools(request.Context(), principal)
	if err != nil {
		writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
		return
	}
	writeRPCResult(response, message.id, struct {
		Tools []Tool `json:"tools"`
	}{Tools: tools})
}

func (h *handler) handleToolsCall(response http.ResponseWriter, request *http.Request, principal Principal, message incomingMessage) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Task      json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(message.params, &params); err != nil || params.Name == "" {
		writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
		return
	}
	if len(params.Task) != 0 {
		writeRPCError(response, http.StatusOK, message.id, -32601, "Method not found")
		return
	}
	arguments := ToolArguments{}
	if len(params.Arguments) != 0 {
		if !isJSONObject(params.Arguments) || json.Unmarshal(params.Arguments, &arguments) != nil {
			writeRPCError(response, http.StatusOK, message.id, -32602, "Invalid params")
			return
		}
	}

	tools, err := h.listTools(request.Context(), principal)
	if err != nil {
		writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
		return
	}
	if !containsTool(tools, params.Name) {
		writeRPCError(response, http.StatusOK, message.id, -32602, "Unknown tool")
		return
	}

	result, err := h.dispatcher.CallTool(request.Context(), principal, params.Name, arguments)
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			writeRPCError(response, http.StatusOK, message.id, -32602, "Unknown tool")
			return
		}
		var executionErr *ToolExecutionError
		if errors.As(err, &executionErr) {
			if executionErr == nil {
				writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
				return
			}
			messageText := executionErr.Message
			if messageText == "" {
				messageText = "Tool execution failed"
			}
			writeRPCResult(response, message.id, ToolResult{
				Content: []ContentBlock{TextContent(messageText)},
				IsError: true,
			})
			return
		}
		writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
		return
	}
	if result.Content == nil {
		result.Content = []ContentBlock{}
	}
	if !validContent(result.Content) {
		writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
		return
	}
	if _, err := json.Marshal(result); err != nil {
		writeRPCError(response, http.StatusOK, message.id, -32603, "Internal error")
		return
	}
	writeRPCResult(response, message.id, result)
}

func (h *handler) listTools(ctx context.Context, principal Principal) ([]Tool, error) {
	tools, err := h.registry.ListTools(ctx, principal)
	if err != nil {
		return nil, err
	}
	tools = append([]Tool(nil), tools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	for index := range tools {
		if err := validateTool(tools[index]); err != nil {
			return nil, err
		}
		if index > 0 && tools[index-1].Name == tools[index].Name {
			return nil, errors.New("mcp: duplicate tool name")
		}
	}
	if tools == nil {
		tools = []Tool{}
	}
	return tools, nil
}

func (h *handler) authenticate(response http.ResponseWriter, request *http.Request) (Principal, bool) {
	// Public bearer credentials terminate at Oathkeeper. Seeing one here means
	// the gateway failed to scrub it or this handler was reached directly. Ory
	// may preserve the header key with an exact empty value after scrubbing, so
	// reject residual values rather than key presence.
	if hasNonEmptyHeaderValue(request.Header.Values("Authorization")) {
		writeHTTPError(response, http.StatusUnauthorized, "Unauthorized")
		return Principal{}, false
	}
	principal, ok := PrincipalFromContext(request.Context())
	if !ok {
		writeHTTPError(response, http.StatusUnauthorized, "Unauthorized")
		return Principal{}, false
	}
	return principal, true
}

func hasNonEmptyHeaderValue(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func (h *handler) protocolVersionAllowed(request *http.Request, method string) bool {
	values := request.Header.Values("MCP-Protocol-Version")
	if method == "initialize" && len(values) == 0 {
		return true
	}
	return len(values) == 1 && strings.TrimSpace(values[0]) == ProtocolVersion
}

func (h *handler) originAllowed(values []string) bool {
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	canonical, ok := canonicalOrigin(values[0])
	if !ok {
		return false
	}
	_, ok = h.allowedOrigins[canonical]
	return ok
}

type incomingMessage struct {
	method string
	params json.RawMessage
	id     json.RawMessage
	hasID  bool
}

func (message incomingMessage) responseID() json.RawMessage {
	if !message.hasID {
		return nil
	}
	return message.id
}

type protocolError struct {
	code    int
	message string
	status  int
	id      json.RawMessage
}

func parseMessage(body []byte) (incomingMessage, *protocolError) {
	if !utf8.Valid(body) {
		return incomingMessage{}, &protocolError{code: -32700, message: "Parse error", status: http.StatusBadRequest}
	}
	body = []byte(strings.TrimSpace(string(body)))
	if !json.Valid(body) {
		return incomingMessage{}, &protocolError{code: -32700, message: "Parse error", status: http.StatusBadRequest}
	}
	if len(body) == 0 || body[0] != '{' {
		return incomingMessage{}, &protocolError{code: -32600, message: "Invalid Request", status: http.StatusBadRequest}
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return incomingMessage{}, &protocolError{code: -32600, message: "Invalid Request", status: http.StatusBadRequest}
	}
	id, hasID := envelope["id"]
	if hasID && !validRequestID(id) {
		return incomingMessage{}, &protocolError{code: -32600, message: "Invalid Request", status: http.StatusBadRequest}
	}
	var version string
	if err := json.Unmarshal(envelope["jsonrpc"], &version); err != nil || version != "2.0" {
		return incomingMessage{}, &protocolError{code: -32600, message: "Invalid Request", status: http.StatusBadRequest, id: id}
	}
	var method string
	if err := json.Unmarshal(envelope["method"], &method); err != nil || method == "" {
		return incomingMessage{}, &protocolError{code: -32600, message: "Invalid Request", status: http.StatusBadRequest, id: id}
	}

	message := incomingMessage{method: method, params: envelope["params"], id: id, hasID: hasID}
	if len(message.params) != 0 && !isJSONObject(message.params) {
		return incomingMessage{}, &protocolError{code: -32602, message: "Invalid params", status: http.StatusOK, id: id}
	}
	return message, nil
}

func validRequestID(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return true
	case json.Number:
		_, err := strconv.ParseFloat(string(typed), 64)
		return err == nil
	default:
		return false
	}
}

func validateTool(tool Tool) error {
	if len(tool.Name) < 1 || len(tool.Name) > 128 {
		return errors.New("mcp: invalid tool name length")
	}
	for _, character := range tool.Name {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.') {
			return errors.New("mcp: invalid tool name")
		}
	}
	if !isObjectSchema(tool.InputSchema) {
		return errors.New("mcp: invalid tool input schema")
	}
	if len(tool.OutputSchema) != 0 && !isObjectSchema(tool.OutputSchema) {
		return errors.New("mcp: invalid tool output schema")
	}
	if len(tool.SecuritySchemes) != 1 || tool.SecuritySchemes[0].Type != "oauth2" ||
		len(tool.SecuritySchemes[0].Scopes) != 2 || tool.SecuritySchemes[0].Scopes[0] != "mcp" ||
		tool.SecuritySchemes[0].Scopes[1] != "offline_access" {
		return errors.New("mcp: tool securitySchemes must be exactly oauth2 with mcp and offline_access scopes")
	}
	mirroredValue, ok := tool.Meta["securitySchemes"]
	if !ok {
		return errors.New("mcp: _meta.securitySchemes mirror is required")
	}
	mirrored, ok := mirroredValue.([]ToolSecurityScheme)
	if !ok || !equalSecuritySchemes(tool.SecuritySchemes, mirrored) {
		return errors.New("mcp: _meta.securitySchemes must exactly mirror securitySchemes")
	}
	for _, name := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
		if _, ok := tool.Annotations[name].(bool); !ok {
			return fmt.Errorf("mcp: tool annotation %s must be an explicit boolean", name)
		}
	}
	if tool.Annotations["readOnlyHint"] == true && tool.Annotations["destructiveHint"] == true {
		return errors.New("mcp: a read-only tool cannot be destructive")
	}
	if _, err := json.Marshal(tool); err != nil {
		return errors.New("mcp: tool definition is not JSON encodable")
	}
	return nil
}

func equalSecuritySchemes(left, right []ToolSecurityScheme) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Type != right[index].Type || len(left[index].Scopes) != len(right[index].Scopes) {
			return false
		}
		for scopeIndex := range left[index].Scopes {
			if left[index].Scopes[scopeIndex] != right[index].Scopes[scopeIndex] {
				return false
			}
		}
	}
	return true
}

func isObjectSchema(raw json.RawMessage) bool {
	if !isJSONObject(raw) {
		return false
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil {
		return false
	}
	var schemaType string
	if err := json.Unmarshal(schema["type"], &schemaType); err != nil {
		return false
	}
	return schemaType == "object"
}

func validContent(content []ContentBlock) bool {
	for _, block := range content {
		blockType, ok := block["type"].(string)
		if !ok || blockType == "" {
			return false
		}
	}
	return true
}

func containsTool(tools []Tool, name string) bool {
	index := sort.Search(len(tools), func(index int) bool { return tools[index].Name >= name })
	return index < len(tools) && tools[index].Name == name
}

func paramsAreObjectOrAbsent(params json.RawMessage) bool {
	return len(params) == 0 || isJSONObject(params)
}

func isJSONObject(raw json.RawMessage) bool {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	return len(raw) > 0 && raw[0] == '{' && json.Valid(raw)
}

func isJSONContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	if charset, present := params["charset"]; present && !strings.EqualFold(charset, "utf-8") {
		return false
	}
	return true
}

func accepts(values []string, wanted string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil || !strings.EqualFold(mediaType, wanted) {
				continue
			}
			if quality, ok := params["q"]; ok {
				parsed, err := strconv.ParseFloat(quality, 64)
				if err != nil || parsed <= 0 || parsed > 1 {
					continue
				}
			}
			return true
		}
	}
	return false
}

func canonicalOrigin(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}

type rpcErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErrorBody   `json:"error,omitempty"`
}

func writeRPCResult(response http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(response, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeRPCError(response http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeJSON(response, status, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcErrorBody{Code: code, Message: message},
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		writeHTTPError(response, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(append(encoded, '\n'))
}

func writeHTTPError(response http.ResponseWriter, status int, message string) {
	response.Header().Set("Cache-Control", "no-store")
	http.Error(response, message, status)
}
