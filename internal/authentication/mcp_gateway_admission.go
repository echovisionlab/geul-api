package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const MCPGatewayAuthorAdmissionPath = "/internal/mcp/admission/is-author"

const mcpGatewayAdmissionAttribution = "oathkeeper-mcp-admission"

// GatewayAdmissionChecker is the one typed authorization dependency used by
// the Oathkeeper MCP admission boundary.
type GatewayAdmissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
}

// NewMCPGatewayAuthorAdmissionHandler builds the private Oathkeeper
// remote_json authorizer. Oathkeeper has already authenticated the credential;
// this boundary accepts only its trusted account identity and performs one
// current Platform.IsAuthor decision.
func NewMCPGatewayAuthorAdmissionHandler(
	internalServiceSecret string,
	authHeaderName string,
	internalServiceHeaderName string,
	checker GatewayAdmissionChecker,
) (http.Handler, error) {
	if strings.TrimSpace(internalServiceSecret) == "" || interfaceValueIsNil(checker) {
		return nil, errors.New("MCP gateway admission dependencies are required")
	}
	if err := auth.ValidateHeaderNames(authHeaderName, internalServiceHeaderName); err != nil {
		return nil, fmt.Errorf("invalid MCP gateway admission header names: %w", err)
	}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.URL.Path != MCPGatewayAuthorAdmissionPath {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !exactTrustedGatewayAdmissionRequest(internalServiceSecret, authHeaderName, internalServiceHeaderName, request) {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}

		accountIdentityID, ok := decodeGatewayAdmissionRequest(response, request)
		if !ok {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		decision, err := mcpGatewayAuthorAdmissionDecision(accountIdentityID)
		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		allowed, err := checker.Can(request.Context(), decision)
		if err != nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			response.WriteHeader(http.StatusForbidden)
			return
		}
		// Oathkeeper v26.2 remote_json accepts exactly HTTP 200 and ignores the
		// response body. Keep the success response empty.
		response.WriteHeader(http.StatusOK)
	}), nil
}

func exactTrustedGatewayAdmissionRequest(secret, authHeaderName, internalServiceHeaderName string, request *http.Request) bool {
	if request == nil || len(request.Header.Values(internalServiceHeaderName)) != 1 ||
		!auth.IsInternalServiceRequest(secret, internalServiceHeaderName, request) {
		return false
	}
	for header := range request.Header {
		lower := strings.ToLower(header)
		if lower == "authorization" || lower == "cookie" || lower == "x-session-id" ||
			lower == "x-member-id" || lower == "x-identity-id" || lower == "x-role" ||
			lower == "x-permission" || lower == strings.ToLower(authHeaderName) {
			return false
		}
	}
	return true
}

func decodeGatewayAdmissionRequest(response http.ResponseWriter, request *http.Request) (string, bool) {
	request.Body = http.MaxBytesReader(response, request.Body, 256)
	decoder := json.NewDecoder(request.Body)
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return "", false
	}
	seen := false
	accountIdentityID := ""
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || key != "account_identity_id" || seen {
			return "", false
		}
		if err := decoder.Decode(&accountIdentityID); err != nil {
			return "", false
		}
		seen = true
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || !seen {
		return "", false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", false
	}
	identityID, err := uuidutil.ParseCanonical(accountIdentityID, "account_identity_id")
	if err != nil {
		return "", false
	}
	return identityID.String(), true
}

func mcpGatewayAuthorAdmissionDecision(accountIdentityID string) (policyv1.AuthorizationDecision, error) {
	actor, err := policyv1.NewAccountIdentityActor(accountIdentityID)
	if err != nil {
		return policyv1.AuthorizationDecision{}, err
	}
	// AuthorizationDecision requires attribution, while this private boundary
	// deliberately receives no bearer, session, client, or PAT fields. This
	// fixed transport attribution cannot select or alter the engine permission.
	delegation, err := policyv1.DirectSession(mcpGatewayAdmissionAttribution)
	if err != nil {
		return policyv1.AuthorizationDecision{}, err
	}
	can, err := policyv1.Platform.IsAuthor()
	if err != nil {
		return policyv1.AuthorizationDecision{}, err
	}
	return policyv1.NewAuthorizationDecision(actor, delegation, can)
}

func interfaceValueIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflection := reflect.ValueOf(value)
	switch reflection.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflection.IsNil()
	default:
		return false
	}
}
