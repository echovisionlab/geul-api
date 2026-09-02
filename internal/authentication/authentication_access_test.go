package authentication

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

const (
	authenticationTestSessionID  = "11111111-1111-4111-8111-111111111111"
	authenticationTestIdentityID = "22222222-2222-4222-8222-222222222222"
	authenticationTestMemberID   = "33333333-3333-4333-8333-333333333333"
)

type authenticationAccessWriterStub struct {
	records []sharedtelemetry.SecurityAccessRecord
	err     error
}

func (writer *authenticationAccessWriterStub) AppendSecurityAccess(
	_ context.Context,
	record sharedtelemetry.SecurityAccessRecord,
) error {
	writer.records = append(writer.records, record)
	return writer.err
}

func TestAuthenticationAccessRecordsSuccessBeforeExposingSession(t *testing.T) {
	writer := &authenticationAccessWriterStub{}
	recorder := newAuthenticationAccessTestRecorder(writer)
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "ory_kratos_session=secret; Path=/; HttpOnly")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{"id":"` + authenticationTestSessionID + `"},"session_token":"secret"}`))
	}))

	request := authenticationAccessRequest(
		http.MethodPost,
		"https://auth.example/self-service/login?flow=login-flow",
		`{"method":"code","code":"123456"}`,
	)
	response := &appendOrderResponseWriter{ResponseRecorder: httptest.NewRecorder(), writer: writer, t: t}
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Len(t, writer.records, 1)
	record := writer.records[0]
	require.Equal(t, sharedtelemetry.SecurityAuthenticationSucceeded, record.Action)
	require.Equal(t, sharedtelemetry.AuthenticationFlowLogin, record.FlowKind)
	require.Equal(t, sharedtelemetry.AuthenticationMethodEmailCode, record.AuthenticationMethod)
	require.Equal(t, sharedtelemetry.AuthenticationPrincipalActive, record.PrincipalState)
	require.Equal(t, sharedtelemetry.ActorKindMember, record.Kind)
	require.Equal(t, authenticationTestMemberID, record.MemberID)
	require.Equal(t, "192.0.2.44", record.SourceIP)
}

func TestAuthenticationAccessSuppressesSuccessWhenAppendFails(t *testing.T) {
	writer := &authenticationAccessWriterStub{err: errors.New("database unavailable")}
	recorder := newAuthenticationAccessTestRecorder(writer)
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "ory_kratos_session=secret; Path=/; HttpOnly")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{"id":"` + authenticationTestSessionID + `"},"session_token":"secret"}`))
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticationAccessRequest(
		http.MethodPost,
		"https://auth.example/self-service/login?flow=login-flow",
		`{"method":"code","code":"123456"}`,
	))

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Len(t, writer.records, 1)
	require.Empty(t, response.Header().Values("Set-Cookie"))
	require.NotContains(t, response.Body.String(), "session_token")
	require.NotContains(t, response.Body.String(), "secret")
}

func TestAuthenticationAccessPreservesDeniedResponseWhenAppendFails(t *testing.T) {
	writer := &authenticationAccessWriterStub{err: errors.New("database unavailable")}
	recorder := newAuthenticationAccessTestRecorder(writer)
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ui":{"messages":[{"id":4010008,"type":"error","text":"sensitive"}]}}`))
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticationAccessRequest(
		http.MethodPost,
		"https://auth.example/self-service/login?flow=login-flow",
		`{"method":"code","code":"000000"}`,
	))

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "4010008")
	require.Len(t, writer.records, 1)
	require.Equal(t, sharedtelemetry.SecurityAuthenticationFailed, writer.records[0].Action)
	require.Equal(t, string(sharedtelemetry.AuthenticationFailureProofRejected), writer.records[0].Reason)
}

func TestAuthenticationAccessDoesNotRecordIntermediateChallenges(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		response string
		status   int
	}{
		{name: "email code delivery", body: `{"method":"code","identifier":"person@example.test"}`, response: `{"state":"sent_email"}`, status: http.StatusBadRequest},
		{name: "oidc redirect", body: `{"provider":"google"}`, response: `{"error":{"id":"browser_location_change_required"}}`, status: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &authenticationAccessWriterStub{}
			recorder := newAuthenticationAccessTestRecorder(writer)
			handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authenticationAccessRequest(
				http.MethodPost,
				"https://auth.example/self-service/login?flow=login-flow",
				tt.body,
			))
			require.Equal(t, tt.status, response.Code)
			require.Empty(t, writer.records)
		})
	}
}

func TestAuthenticationAccessClassifiesOIDCFirstSessionAsRegistration(t *testing.T) {
	writer := &authenticationAccessWriterStub{}
	recorder := newAuthenticationAccessTestRecorder(writer)
	recorder.firstSession = func(context.Context, string) (bool, error) { return true, nil }
	recorder.resolvePrincipal = func(context.Context, string) (*auth.UserInfo, error) {
		return &auth.UserInfo{
			SessionID: auth.SessionID(authenticationTestSessionID), IdentityID: auth.IdentityID(authenticationTestIdentityID),
			MemberID: auth.MemberID(authenticationTestMemberID), Authenticated: true, Onboarded: false,
		}, nil
	}
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session":{"id":"` + authenticationTestSessionID + `"}}`))
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticationAccessRequest(
		http.MethodGet,
		"https://auth.example/self-service/methods/oidc/callback/google?code=secret&state=secret",
		"",
	))

	require.Equal(t, http.StatusOK, response.Code)
	require.Len(t, writer.records, 1)
	require.Equal(t, sharedtelemetry.SecurityAuthenticationSucceeded, writer.records[0].Action)
	require.Equal(t, sharedtelemetry.AuthenticationFlowRegistration, writer.records[0].FlowKind)
	require.Equal(t, sharedtelemetry.AuthenticationMethodOIDC, writer.records[0].AuthenticationMethod)
	require.Equal(t, sharedtelemetry.AuthenticationPrincipalOnboardingOnly, writer.records[0].PrincipalState)
	require.Equal(t, "google", writer.records[0].Provider)
}

func TestObserveUnifiedAuthenticationUsesRefreshAsReauthentication(t *testing.T) {
	observation := &authenticationObservation{Candidate: true}
	ctx := context.WithValue(context.Background(), authenticationObservationKey{}, observation)
	observeUnifiedAuthentication(
		ctx,
		unifiedAuthLoginFlow,
		true,
		"application/x-www-form-urlencoded",
		[]byte("code=123456&method=code"),
	)
	require.Equal(t, sharedtelemetry.AuthenticationFlowReauthentication, observation.FlowKind)
	require.Equal(t, sharedtelemetry.AuthenticationMethodEmailCode, observation.Method)
	require.True(t, observation.ProofSubmitted)
}

func TestAuthenticationTerminalMappingsUseOnlyStableIdentifiers(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		expected sharedtelemetry.SecurityAction
		reason   string
	}{
		{name: "csrf", status: http.StatusForbidden, body: `{"error":{"id":"security_csrf_violation","reason":"raw"}}`, expected: sharedtelemetry.SecurityAuthenticationBlocked, reason: string(sharedtelemetry.AuthenticationBlockIntegrityCheckFailed)},
		{name: "expired flow", status: http.StatusGone, body: `{"error":{"id":"self_service_flow_expired"}}`, expected: sharedtelemetry.SecurityAuthenticationBlocked, reason: string(sharedtelemetry.AuthenticationBlockFlowInvalid)},
		{name: "rate limit", status: http.StatusTooManyRequests, body: `{"error":{"message":"raw"}}`, expected: sharedtelemetry.SecurityAuthenticationBlocked, reason: string(sharedtelemetry.AuthenticationBlockRateLimited)},
		{name: "proof rejection", status: http.StatusBadRequest, body: `{"ui":{"messages":[{"id":4010008,"text":"raw"}]}}`, expected: sharedtelemetry.SecurityAuthenticationFailed, reason: string(sharedtelemetry.AuthenticationFailureProofRejected)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &authenticationAccessWriterStub{}
			recorder := newAuthenticationAccessTestRecorder(writer)
			request := authenticationAccessRequest(
				http.MethodPost,
				"https://auth.example/self-service/login?flow=login-flow",
				`{"method":"code","code":"000000"}`,
			)
			observation, err := inspectAuthenticationObservation(request)
			require.NoError(t, err)
			recorder.appendTerminalDenial(request, observation, bufferedKratosResponse{
				StatusCode: tt.status, Body: []byte(tt.body),
			})
			require.Len(t, writer.records, 1)
			require.Equal(t, tt.expected, writer.records[0].Action)
			require.Equal(t, tt.reason, writer.records[0].Reason)
			require.NotContains(t, writer.records[0].Reason, "raw")
		})
	}
}

func newAuthenticationAccessTestRecorder(writer securityaccess.Appender) *AuthenticationAccessRecorder {
	return &AuthenticationAccessRecorder{
		writer: writer,
		now:    func() time.Time { return time.Date(2026, time.August, 10, 1, 2, 3, 0, time.UTC) },
		resolvePrincipal: func(context.Context, string) (*auth.UserInfo, error) {
			return &auth.UserInfo{
				SessionID: auth.SessionID(authenticationTestSessionID), IdentityID: auth.IdentityID(authenticationTestIdentityID),
				MemberID: auth.MemberID(authenticationTestMemberID), Authenticated: true, Onboarded: true,
			}, nil
		},
		firstSession:    func(context.Context, string) (bool, error) { return false, nil },
		resolveIncoming: func(*http.Request) *auth.UserInfo { return nil },
	}
}

func authenticationAccessRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.44")
	if err != nil {
		panic(err)
	}
	return request.WithContext(sharedtelemetry.WithRequestContext(request.Context(), requestContext))
}

type appendOrderResponseWriter struct {
	*httptest.ResponseRecorder
	writer *authenticationAccessWriterStub
	t      *testing.T
}

func (writer *appendOrderResponseWriter) WriteHeader(status int) {
	writer.t.Helper()
	require.NotEmpty(writer.t, writer.writer.records, "security access must append before response headers are exposed")
	writer.ResponseRecorder.WriteHeader(status)
}
