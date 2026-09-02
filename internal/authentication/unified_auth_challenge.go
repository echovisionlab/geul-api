package authentication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (h *UnifiedAuthHandler) handleInitialLoginCodeChallenge(
	w http.ResponseWriter,
	request *http.Request,
	flow unifiedAuthFlow,
	body []byte,
	identifier string,
) {
	_, exists, err := h.identityFinder.FindIdentityByCredentialIdentifier(request.Context(), identifier)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	blocked := false
	if !exists {
		blocked, err = h.registrationEmailReuseBlocked(request.Context(), identifier)
		if err != nil {
			writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
			return
		}
	}
	h.submitInitialCodeChallenge(w, request, flow, body, exists, blocked, identifier)
}

func (h *UnifiedAuthHandler) handleBlockedRegistrationCode(
	w http.ResponseWriter,
	request *http.Request,
	flowResponse bufferedKratosResponse,
	flow unifiedAuthFlow,
	identifier string,
	code string,
) bool {
	registrationEmail := flow.nodeString("traits.email")
	if registrationEmail == "" {
		registrationEmail = identifier
	}
	if registrationEmail == "" {
		writeKratosProxyError(w, http.StatusBadRequest, "authentication code request is invalid", 0)
		return true
	}
	blocked, err := h.registrationEmailReuseBlocked(request.Context(), registrationEmail)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return true
	}
	if !blocked {
		return false
	}
	if strings.TrimSpace(code) == "" {
		h.copyUndeliveredCodeChallenge(w, flowResponse, flow, registrationEmail)
		return true
	}
	markAuthenticationFacadeBlock(request.Context(), sharedtelemetry.AuthenticationBlockRequestInvalid)
	h.copyUndeliveredInvalidCode(w, flowResponse, flow, registrationEmail)
	return true
}

// registrationEmailReuseBlocked is checked before deciding whether a code
// request is login or registration. It never resurrects an old identity or
// Member link.
func (h *UnifiedAuthHandler) registrationEmailReuseBlocked(ctx context.Context, email string) (bool, error) {
	if h.reuseHoldCheck == nil {
		return false, fmt.Errorf("registration email reuse hold checker is required")
	}
	blocked, err := h.reuseHoldCheck(ctx, email)
	if err != nil {
		return false, fmt.Errorf("check deleted identity email reuse hold: %w", err)
	}
	return blocked, nil
}

func (h *UnifiedAuthHandler) submitInitialCodeChallenge(
	w http.ResponseWriter,
	request *http.Request,
	loginFlow unifiedAuthFlow,
	originalBody []byte,
	identityExists bool,
	registrationBlocked bool,
	identifier string,
) {
	query := make(url.Values)
	if strings.TrimSpace(loginFlow.ReturnTo) != "" {
		query.Set("return_to", loginFlow.ReturnTo)
	}
	registrationResponse := h.transport.capture(
		request,
		http.MethodGet,
		"/self-service/registration/browser",
		query,
		nil,
		true,
	)
	if registrationResponse.StatusCode != http.StatusOK {
		h.copyResponse(w, registrationResponse)
		return
	}
	registrationFlow, err := decodeUnifiedAuthFlow(registrationResponse.Body)
	if err != nil || registrationFlow.ID == "" {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	if registrationBlocked {
		h.copyUndeliveredCodeChallenge(w, registrationResponse, registrationFlow, identifier)
		return
	}

	path := "/self-service/registration"
	flowID := registrationFlow.ID
	payload := originalBody
	contentType := request.Header.Get("Content-Type")
	if identityExists {
		path = "/self-service/login"
		flowID = loginFlow.ID
	} else {
		payload, err = buildRegistrationCodePayload(originalBody, contentType, registrationFlow, true)
		if err != nil {
			writeKratosProxyError(w, http.StatusBadRequest, "authentication code request is invalid", 0)
			return
		}
		contentType = "application/json"
	}

	kratosCookies := (&http.Response{Header: registrationResponse.Header}).Cookies()
	cookies := mergeCookies(request.Cookies(), kratosCookies)
	forwardRequest := request.Clone(request.Context())
	forwardRequest.Header = request.Header.Clone()
	forwardRequest.Header.Set("Content-Type", contentType)
	forwardRequest.Header.Del("Cookie")
	for _, cookie := range cookies {
		forwardRequest.AddCookie(cookie)
	}

	capture := h.transport.capture(
		forwardRequest,
		http.MethodPost,
		path,
		url.Values{"flow": {flowID}},
		payload,
		false,
	)
	responseHeader := capture.Header.Clone()
	responseHeader.Del("Set-Cookie")
	for _, cookie := range mergeUnifiedAuthSetCookies(registrationResponse.Header, capture.Header) {
		responseHeader.Add("Set-Cookie", cookie)
	}
	h.copyResponse(w, bufferedKratosResponse{
		StatusCode: capture.StatusCode,
		Header:     responseHeader,
		Body:       capture.Body,
	})
}

// copyUndeliveredCodeChallenge returns the same public challenge surface as a
// delivered code without submitting the held address to Kratos. The real
// registration flow and CSRF cookie remain the authority for later requests;
// every resend or completion attempt is checked against the hold again.
func (h *UnifiedAuthHandler) copyUndeliveredCodeChallenge(
	w http.ResponseWriter,
	response bufferedKratosResponse,
	flow unifiedAuthFlow,
	identifier string,
) {
	h.copyUndeliveredCodeFlow(w, response, flow, identifier, "info")
}

func (h *UnifiedAuthHandler) copyUndeliveredInvalidCode(
	w http.ResponseWriter,
	response bufferedKratosResponse,
	flow unifiedAuthFlow,
	identifier string,
) {
	h.copyUndeliveredCodeFlow(w, response, flow, identifier, "error")
}

func (h *UnifiedAuthHandler) copyUndeliveredCodeFlow(
	w http.ResponseWriter,
	response bufferedKratosResponse,
	flow unifiedAuthFlow,
	identifier string,
	messageType string,
) {
	var payload unifiedAuthObject
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	ui, ok := payload["ui"].(unifiedAuthObject)
	if !ok {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	ui["nodes"] = unifiedAuthValues{
		unifiedAuthInputNode("csrf_token", flow.nodeString("csrf_token"), "default", "hidden"),
		unifiedAuthInputNode("identifier", email.NormalizeAddressForDelivery(identifier), "default", "hidden"),
		unifiedAuthInputNode("code", "", "code", "text"),
		unifiedAuthInputNode("method", "code", "code", "submit"),
		unifiedAuthInputNode("resend", "code", "code", "submit"),
	}
	ui["method"] = http.MethodPost
	ui["messages"] = unifiedAuthValues{unifiedAuthObject{"type": messageType, "text": ""}}
	payload["active"] = "code"
	payload["state"] = "sent_email"
	body, err := json.Marshal(payload)
	if err != nil {
		writeKratosProxyError(w, http.StatusServiceUnavailable, "authentication service is temporarily unavailable", 0)
		return
	}
	response.StatusCode = http.StatusBadRequest
	response.Body = body
	response.Header = response.Header.Clone()
	response.Header.Del("Location")
	h.copyResponse(w, response)
}

func mergeCookies(groups ...[]*http.Cookie) []*http.Cookie {
	byName := make(map[string]*http.Cookie)
	order := make([]string, 0)
	for _, cookies := range groups {
		for _, cookie := range cookies {
			if cookie == nil || cookie.Name == "" {
				continue
			}
			if _, exists := byName[cookie.Name]; !exists {
				order = append(order, cookie.Name)
			}
			copy := *cookie
			byName[cookie.Name] = &copy
		}
	}
	merged := make([]*http.Cookie, 0, len(order))
	for _, name := range order {
		merged = append(merged, byName[name])
	}
	return merged
}
