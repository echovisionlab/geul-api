package authentication

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"gorm.io/gorm"
)

type AuthenticationAccessRecorder struct {
	target           *url.URL
	httpClient       *http.Client
	db               *gorm.DB
	writer           securityaccess.Appender
	now              func() time.Time
	resolvePrincipal func(context.Context, string) (*auth.UserInfo, error)
	firstSession     func(context.Context, string) (bool, error)
	resolveIncoming  func(*http.Request) *auth.UserInfo
}

func NewAuthenticationAccessRecorder(
	kratosPublicURL string,
	db *gorm.DB,
	writer securityaccess.Appender,
) (*AuthenticationAccessRecorder, error) {
	if db == nil {
		return nil, errors.New("authentication access database is required")
	}
	if writer == nil {
		return nil, errors.New("security access writer is required")
	}
	target, err := parseKratosPublicURL(kratosPublicURL)
	if err != nil {
		return nil, err
	}
	recorder := &AuthenticationAccessRecorder{
		target:     target,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		db:         db,
		writer:     writer,
		now:        func() time.Time { return time.Now().UTC() },
	}
	recorder.resolvePrincipal = func(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
		return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, sessionID)
	}
	recorder.firstSession = func(ctx context.Context, sessionID string) (bool, error) {
		return firstIssuedIdentitySession(ctx, db, sessionID)
	}
	recorder.resolveIncoming = recorder.resolveIncomingMember
	return recorder, nil
}

func (recorder *AuthenticationAccessRecorder) Wrap(next http.Handler) http.Handler {
	if next == nil {
		panic("authentication access handler is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observation, bodyErr := inspectAuthenticationObservation(request)
		if !observation.Candidate {
			next.ServeHTTP(w, request)
			return
		}
		ctx := context.WithValue(request.Context(), authenticationObservationKey{}, observation)
		request = request.WithContext(ctx)

		var response bufferedKratosResponse
		if bodyErr != nil {
			response = bufferedAuthenticationError(http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			capture := newKratosResponseCapture(nil, maxKratosProxyRequestBytes)
			next.ServeHTTP(capture, request)
			response = bufferedKratosResponse{
				StatusCode: capture.statusCode(),
				Header:     capture.Header().Clone(),
				Body:       append([]byte(nil), capture.body.Bytes()...),
			}
		}

		if authenticationResponseIsIntermediate(observation, response) {
			copyBufferedKratosResponse(w, response, nil)
			return
		}
		recorder.complete(w, request, observation, response)
	})
}
