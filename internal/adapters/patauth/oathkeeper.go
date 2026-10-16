package patauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

const OathkeeperPath = "/internal/oathkeeper/pat"

// OathkeeperHandler implements bearer_token's session-check contract. Register
// it only on the private listener; the separate internal-service credential
// authenticates Oathkeeper before any Member credential is checked.
type OathkeeperHandler struct {
	authenticator Authenticator
	serviceSecret string
	serviceHeader string
	slots         chan struct{}
}

type oathkeeperResponse struct {
	Subject pat.MemberID `json:"subject"`
	Extra   struct {
		MemberID pat.MemberID `json:"member_id"`
		TokenID  pat.TokenID  `json:"token_id"`
	} `json:"extra"`
}

func NewOathkeeper(secret, header string, authenticator Authenticator) (*OathkeeperHandler, error) {
	normalized, err := auth.NormalizeHeaderName(header)
	if err != nil || normalized == "Authorization" || strings.TrimSpace(secret) == "" || authenticator == nil {
		return nil, errors.New("Oathkeeper PAT verification requires an internal credential, a separate header and an authenticator")
	}
	return &OathkeeperHandler{
		authenticator: authenticator, serviceSecret: secret, serviceHeader: normalized,
		slots: make(chan struct{}, maximumConcurrentChecks),
	}, nil
}

func (handler *OathkeeperHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	if request.URL.Path != OathkeeperPath {
		writeOathkeeperError(writer, http.StatusNotFound)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeOathkeeperError(writer, http.StatusMethodNotAllowed)
		return
	}
	if len(request.Header.Values(handler.serviceHeader)) != 1 ||
		!auth.IsInternalServiceRequest(handler.serviceSecret, handler.serviceHeader, request) {
		handler.fail(writer, request, http.StatusUnauthorized, errors.New("untrusted Oathkeeper PAT caller"), "")
		return
	}
	token, ok := oathkeeperBearer(request)
	if !ok || request.URL.RawQuery != "" {
		handler.fail(writer, request, http.StatusUnauthorized, pat.ErrInvalidToken, token)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1))
	if err != nil || len(body) != 0 {
		handler.fail(writer, request, http.StatusUnauthorized, errors.New("Oathkeeper PAT check requires an empty body"), token)
		return
	}
	select {
	case handler.slots <- struct{}{}:
		defer func() { <-handler.slots }()
	default:
		writer.Header().Set("Retry-After", "1")
		handler.fail(writer, request, http.StatusServiceUnavailable, errors.New("Oathkeeper PAT verification saturated"), token)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), authenticationTimeout)
	defer cancel()
	// Always consult the same current Member PAT authority as the legacy
	// verifier. Neither successful nor failed authentication is cached.
	principal, err := handler.authenticator.Authenticate(ctx, token)
	if errors.Is(err, pat.ErrInvalidToken) {
		handler.fail(writer, request, http.StatusUnauthorized, err, token)
		return
	}
	if err != nil {
		handler.fail(writer, request, http.StatusServiceUnavailable, err, token)
		return
	}
	if principal.MemberID == "" || principal.TokenID == "" {
		handler.fail(writer, request, http.StatusServiceUnavailable, pat.ErrInvalidStoredToken, token)
		return
	}
	response := oathkeeperResponse{Subject: principal.MemberID}
	response.Extra.MemberID = principal.MemberID
	response.Extra.TokenID = principal.TokenID
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		slog.ErrorContext(request.Context(), "Oathkeeper PAT response failed", failurecause.Attr(err, token, handler.serviceSecret))
	}
}

func oathkeeperBearer(request *http.Request) (string, bool) {
	if len(request.Header.Values("Authorization")) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(request.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) > maximumCredentialLength ||
		!strings.HasPrefix(token, "pat_") || strings.ContainsAny(token, " \t\r\n,") {
		return "", false
	}
	return token, true
}

func (handler *OathkeeperHandler) fail(writer http.ResponseWriter, request *http.Request, status int, err error, token string) {
	// Private values are redacted even if a dependency includes them in an
	// error. Never attach request headers or bearer values to log records.
	slog.WarnContext(request.Context(), "Oathkeeper PAT verification failed", failurecause.Attr(err, token, handler.serviceSecret), "status", status)
	if status == http.StatusUnauthorized {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="Oathkeeper PAT"`)
	}
	writeOathkeeperError(writer, status)
}

func writeOathkeeperError(writer http.ResponseWriter, status int) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Error string `json:"error"`
	}{Error: http.StatusText(status)})
}
