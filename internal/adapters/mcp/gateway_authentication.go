package mcp

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"google.golang.org/protobuf/proto"
)

const (
	maxAuthenticatedContextEncodedBytes = 3500
)

// NewGatewayAssertionHandler validates Oathkeeper's canonical assertion once,
// removes all ingress-only headers, and binds an MCP Principal. It has no PAT
// verifier or Member persistence dependency.
func NewGatewayAssertionHandler(
	internalServiceSecret string,
	authHeaderName string,
	internalServiceHeaderName string,
	next http.Handler,
) (http.Handler, error) {
	if strings.TrimSpace(internalServiceSecret) == "" || interfaceValueIsNil(next) {
		return nil, ErrInvalidDependency
	}
	if err := auth.ValidateHeaderNames(authHeaderName, internalServiceHeaderName); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDependency, err)
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if hasNonEmptyHeaderValue(request.Header.Values("Authorization")) ||
			hasNonEmptyHeaderValue(request.Header.Values("Cookie")) ||
			hasNonEmptyHeaderValue(request.Header.Values("X-Session-Id")) ||
			!exactInternalServiceRequest(internalServiceSecret, internalServiceHeaderName, request) {
			writeAuthenticationHTTPError(response, http.StatusUnauthorized)
			return
		}
		principal, ok := decodeAuthenticatedContext(authHeaderName, request)
		if !ok {
			writeAuthenticationHTTPError(response, http.StatusUnauthorized)
			return
		}
		ctx, err := mcpserver.WithPrincipal(request.Context(), principal)
		if err != nil {
			writeAuthenticationHTTPError(response, http.StatusUnauthorized)
			return
		}
		request = request.WithContext(ctx)
		for _, header := range []string{
			"Authorization",
			"Cookie",
			"X-Session-Id",
			internalServiceHeaderName,
			authHeaderName,
		} {
			request.Header.Del(header)
		}
		next.ServeHTTP(response, request)
	}), nil
}

func decodeAuthenticatedContext(authHeaderName string, request *http.Request) (mcpserver.Principal, bool) {
	encoded, ok := exactSingleHeader(request, authHeaderName)
	if !ok || len(encoded) > maxAuthenticatedContextEncodedBytes || len(encoded)%4 == 1 {
		return mcpserver.Principal{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		clear(raw)
		return mcpserver.Principal{}, false
	}
	var assertion intrav1.MCPAuthenticatedContext
	err = proto.Unmarshal(raw, &assertion)
	clear(raw)
	if err != nil || len(assertion.ProtoReflect().GetUnknown()) != 0 ||
		assertion.GetDelegationMethod() != intrav1.MCPDelegationMethod_MCP_DELEGATION_METHOD_OAUTH {
		return mcpserver.Principal{}, false
	}
	principal := mcpserver.Principal{
		IdentityID: assertion.GetIdentityId(), MemberID: assertion.GetMemberId(),
		DelegationID: assertion.GetDelegationId(), DelegationName: assertion.GetDelegationName(),
		DelegationMethod: mcpserver.DelegationMethodMCPOAuth,
	}
	return principal, validPrincipal(principal)
}

func validPrincipal(principal mcpserver.Principal) bool {
	return validDelegationMethod(principal.DelegationMethod) &&
		validDelegationID(principal.DelegationMethod, principal.DelegationID) &&
		validDelegationText(principal.DelegationName, 400, 100) &&
		canonicalUUID(principal.IdentityID) && canonicalUUID(principal.MemberID)
}

func validDelegationMethod(method mcpserver.DelegationMethod) bool {
	return method == mcpserver.DelegationMethodMCPOAuth
}

func validDelegationID(method mcpserver.DelegationMethod, value string) bool {
	return method == mcpserver.DelegationMethodMCPOAuth && validOAuthDelegationID(value)
}

func validOAuthDelegationID(value string) bool {
	if !validDelegationText(value, 2048, 0) || !asciiGraphic(value) {
		return false
	}
	if !strings.Contains(value, "://") {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Opaque == "" && parsed.Fragment == "" &&
		parsed.Path != "" && parsed.Path != "/"
}

func asciiGraphic(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func hasNonEmptyHeaderValue(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func exactInternalServiceRequest(secret, headerName string, request *http.Request) bool {
	return len(request.Header.Values(headerName)) == 1 && auth.IsInternalServiceRequest(secret, headerName, request)
}

func exactSingleHeader(request *http.Request, name string) (string, bool) {
	values := request.Header.Values(name)
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != "" && strings.TrimSpace(returnValue) == returnValue
}

func validDelegationText(value string, maxBytes, maxRunes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func writeAuthenticationHTTPError(response http.ResponseWriter, status int) {
	http.Error(response, http.StatusText(status), status)
}
