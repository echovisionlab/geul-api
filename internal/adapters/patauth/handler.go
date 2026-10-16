// Package patauth exposes current Member PAT verification to trusted callers.
// It authenticates a credential; consumers own their authorization decisions.
package patauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

const (
	// Deprecated: legacy Worker verification path. Use OathkeeperPath on the
	// private listener for OpenDiscogs after the Worker is retired.
	Path                    = "/internal/auth/personal-access-token/verify"
	GatewayUsername         = "pat-verifier"
	APIKeyHeader            = "X-API-Key"
	minimumSecretLength     = 32
	maximumSecretLength     = 256
	maximumCredentialLength = 128
	maximumConcurrentChecks = 8
	authenticationTimeout   = 2 * time.Second
)

// Authenticator resolves a current credential through the Member authority.
type Authenticator interface {
	Authenticate(context.Context, string) (pat.Principal, error)
}

type Handler struct {
	authenticator Authenticator
	secretHash    [sha256.Size]byte
	slots         chan struct{}
}

type principalResponse struct {
	MemberID pat.MemberID `json:"member_id"`
}

// New constructs the legacy dedicated-credential verifier.
// Deprecated: use NewOathkeeper for the replacement OpenDiscogs ingress.
func New(secret string, authenticator Authenticator) (*Handler, error) {
	if len(secret) < minimumSecretLength || len(secret) > maximumSecretLength || authenticator == nil {
		return nil, errors.New("PAT verification requires a 32–256 byte gateway secret and an authenticator")
	}
	return &Handler{authenticator: authenticator, secretHash: sha256.Sum256([]byte(secret)), slots: make(chan struct{}, maximumConcurrentChecks)}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != Path {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	username, password, ok := request.BasicAuth()
	candidate := sha256.Sum256([]byte(password))
	if !ok || len(request.Header.Values("Authorization")) != 1 || username != GatewayUsername || subtle.ConstantTimeCompare(candidate[:], handler.secretHash[:]) != 1 {
		writer.Header().Set("WWW-Authenticate", `Basic realm="PAT verification", charset="UTF-8"`)
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if request.URL.RawQuery != "" || len(request.Header.Values(APIKeyHeader)) != 1 || len(request.Header.Get(APIKeyHeader)) > maximumCredentialLength {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1))
	if err != nil || len(body) != 0 {
		if err != nil {
			slog.WarnContext(request.Context(), "Failed to read PAT verification request", "failure_cause", failurecause.Message(err))
		}
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	select {
	case handler.slots <- struct{}{}:
		defer func() { <-handler.slots }()
	default:
		writer.Header().Set("Retry-After", "1")
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), authenticationTimeout)
	defer cancel()
	principal, err := handler.authenticator.Authenticate(ctx, request.Header.Get(APIKeyHeader))
	if errors.Is(err, pat.ErrInvalidToken) {
		slog.WarnContext(ctx, "PAT verification rejected", "failure_cause", failurecause.Message(err, request.Header.Get(APIKeyHeader), password))
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err != nil || principal.MemberID == "" || principal.TokenID == "" {
		if err == nil {
			err = errors.New("PAT authenticator returned an incomplete principal")
		}
		cause := failurecause.Message(err, request.Header.Get(APIKeyHeader), password)
		slog.ErrorContext(ctx, "PAT verification unavailable", "failure_cause", cause)
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(principalResponse{MemberID: principal.MemberID}); err != nil {
		slog.ErrorContext(ctx, "Failed to write PAT verification response", "failure_cause", failurecause.Message(err))
	}
}
