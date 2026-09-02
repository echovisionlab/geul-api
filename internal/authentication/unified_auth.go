package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

const unifiedAuthPath = "/login"

type unifiedAuthIdentityFinder interface {
	FindIdentityByCredentialIdentifier(ctx context.Context, identifier string) (*auth.Identity, bool, error)
}

// RegistrationReuseChecker is the Member-owned retention check that the
// authentication facade needs after credential lookup finds no Email Code
// identity. Authentication owns this narrow consumption contract.
type RegistrationReuseChecker interface {
	RegistrationEmailReuseBlocked(context.Context, string) (bool, error)
}

// RegistrationReuseCheckerFunc adapts a function to RegistrationReuseChecker
// at the composition boundary.
type RegistrationReuseCheckerFunc func(context.Context, string) (bool, error)

// RegistrationEmailReuseBlocked invokes check with the authentication request.
func (check RegistrationReuseCheckerFunc) RegistrationEmailReuseBlocked(ctx context.Context, email string) (bool, error) {
	return check(ctx, email)
}

type unifiedAuthFlowKind string

const (
	unifiedAuthLoginFlow        unifiedAuthFlowKind = "login"
	unifiedAuthRegistrationFlow unifiedAuthFlowKind = "registration"
)

type unifiedAuthFlow struct {
	ID               string            `json:"id"`
	ReturnTo         string            `json:"return_to"`
	Refresh          bool              `json:"refresh"`
	TransientPayload unifiedAuthObject `json:"transient_payload"`
	UI               struct {
		Nodes []struct {
			Attributes struct {
				Name  string           `json:"name"`
				Value unifiedAuthValue `json:"value"`
			} `json:"attributes"`
		} `json:"nodes"`
	} `json:"ui"`
}

func (f unifiedAuthFlow) nodeString(name string) string {
	for _, node := range f.UI.Nodes {
		if node.Attributes.Name == name {
			value, _ := node.Attributes.Value.(string)
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type UnifiedAuthHandler struct {
	transport      *kratosFlowTransport
	identityFinder unifiedAuthIdentityFinder
	reuseHoldCheck func(context.Context, string) (bool, error)
}

func NewUnifiedAuthHandler(
	publicProxy http.Handler,
	identityFinder unifiedAuthIdentityFinder,
	reuseChecker RegistrationReuseChecker,
) (*UnifiedAuthHandler, error) {
	if publicProxy == nil {
		return nil, errors.New("kratos public proxy is required")
	}
	if identityFinder == nil {
		return nil, errors.New("identity credential finder is required")
	}
	if reuseChecker == nil {
		return nil, errors.New("registration reuse checker is required")
	}
	handler := &UnifiedAuthHandler{
		transport:      newKratosFlowTransport(publicProxy),
		identityFinder: identityFinder,
	}
	handler.reuseHoldCheck = func(ctx context.Context, email string) (bool, error) {
		return reuseChecker.RegistrationEmailReuseBlocked(ctx, email)
	}
	return handler, nil
}

func (h *UnifiedAuthHandler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && strings.TrimSuffix(request.URL.Path, "/") == unifiedAuthPath:
		h.transport.forward(w, request, request.Method, "/self-service/login/browser", request.URL.Query(), nil, false)
	case request.Method == http.MethodGet && strings.TrimSuffix(request.URL.Path, "/") == unifiedAuthPath+"/flows":
		h.serveFlow(w, request)
	case request.Method == http.MethodPost && strings.TrimSuffix(request.URL.Path, "/") == unifiedAuthPath:
		h.submitFlow(w, request)
	default:
		http.NotFound(w, request)
	}
}

func (h *UnifiedAuthHandler) serveFlow(w http.ResponseWriter, request *http.Request) {
	flowID := strings.TrimSpace(request.URL.Query().Get("id"))
	if flowID == "" {
		writeKratosProxyError(w, http.StatusBadRequest, "authentication flow id is required", 0)
		return
	}

	_, response, err := h.resolveFlow(request, flowID)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	h.copyResponse(w, response)
}

func (h *UnifiedAuthHandler) submitFlow(w http.ResponseWriter, request *http.Request) {
	flowID := strings.TrimSpace(request.URL.Query().Get("flow"))
	if flowID == "" {
		writeKratosProxyError(w, http.StatusBadRequest, "authentication flow id is required", 0)
		return
	}
	body, err := readBoundedKratosBody(request.Body)
	if err != nil {
		writeKratosProxyError(w, http.StatusRequestEntityTooLarge, "request body is too large", 0)
		return
	}

	kind, flowResponse, err := h.resolveFlow(request, flowID)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	if flowResponse.StatusCode != http.StatusOK {
		h.copyResponse(w, flowResponse)
		return
	}
	flow, err := decodeUnifiedAuthFlow(flowResponse.Body)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}

	method, code, identifier := authCodeRequestFields(request.Header.Get("Content-Type"), body)
	observeUnifiedAuthentication(request.Context(), kind, flow.Refresh, request.Header.Get("Content-Type"), body)
	if kind == unifiedAuthLoginFlow && strings.EqualFold(method, "code") && strings.TrimSpace(code) == "" {
		h.handleInitialLoginCodeChallenge(w, request, flow, body, identifier)
		return
	}
	if kind == unifiedAuthRegistrationFlow && strings.EqualFold(method, "code") {
		if h.handleBlockedRegistrationCode(w, request, flowResponse, flow, identifier, code) {
			return
		}
	}

	path := "/self-service/" + string(kind)
	if kind == unifiedAuthRegistrationFlow && strings.EqualFold(method, "code") {
		body, err = buildRegistrationCodePayload(body, request.Header.Get("Content-Type"), flow, false)
		if err != nil {
			writeKratosProxyError(w, http.StatusBadRequest, "authentication code request is invalid", 0)
			return
		}
		request.Header.Set("Content-Type", "application/json")
	}
	h.copyResponse(w, h.transport.capture(
		request,
		request.Method,
		path,
		url.Values{"flow": {flowID}},
		body,
		false,
	))
}

func (h *UnifiedAuthHandler) resolveFlow(
	request *http.Request,
	flowID string,
) (unifiedAuthFlowKind, bufferedKratosResponse, error) {
	var first bufferedKratosResponse
	for index, kind := range []unifiedAuthFlowKind{unifiedAuthLoginFlow, unifiedAuthRegistrationFlow} {
		response := h.transport.capture(
			request,
			http.MethodGet,
			"/self-service/"+string(kind)+"/flows",
			url.Values{"id": {flowID}},
			nil,
			true,
		)
		if index == 0 {
			first = response
		}
		if response.StatusCode == http.StatusOK {
			return kind, response, nil
		}
	}
	return unifiedAuthLoginFlow, first, nil
}

func decodeUnifiedAuthFlow(body []byte) (unifiedAuthFlow, error) {
	var flow unifiedAuthFlow
	if err := json.Unmarshal(body, &flow); err != nil {
		return flow, err
	}
	if strings.TrimSpace(flow.ID) == "" || len(flow.UI.Nodes) == 0 {
		return flow, errors.New("invalid Kratos authentication flow")
	}
	return flow, nil
}
