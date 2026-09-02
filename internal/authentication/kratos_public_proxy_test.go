package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/cors"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
)

var testAuthIssuanceProvenanceKey = []byte("test-auth-issuance-provenance-secret")

func TestKratosPublicProxyUsesOnlyAPICORSHeaders(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Access-Control-Allow-Origin", "https://www.example.test")
		w.Header().Add("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Access-Control-Expose-Headers", "Content-Type, Set-Cookie")
		w.Header().Add("Vary", "Origin, Cookie")
		_, _ = io.WriteString(w, "webauthn runtime")
	}))
	defer upstream.Close()

	limiter, _ := newTestAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	handler := cors.New(cors.Options{
		AllowedOrigins:   []string{"https://www.example.test"},
		ExposedHeaders:   []string{"Grpc-Status", "Grpc-Message", "Retry-After"},
		AllowCredentials: true,
	}).Handler(proxy)
	server := httptest.NewServer(handler)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/.well-known/ory/webauthn.js", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://www.example.test")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.Equal(t, "webauthn runtime", string(body))
	require.Equal(t, []string{"https://www.example.test"}, response.Header.Values("Access-Control-Allow-Origin"))
	require.Equal(t, []string{"true"}, response.Header.Values("Access-Control-Allow-Credentials"))
	require.Equal(t, []string{"Grpc-Status, Grpc-Message, Retry-After"}, response.Header.Values("Access-Control-Expose-Headers"))
	vary := strings.Join(response.Header.Values("Vary"), ",")
	require.Equal(t, 1, strings.Count(strings.ToLower(vary), "origin"))
	require.Contains(t, vary, "Cookie")
}

func TestKratosPublicProxyLimitsBeforeSecondCodeIssuance(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		require.Equal(t, "/self-service/login", request.URL.Path)
		require.Equal(t, "login-flow", request.URL.Query().Get("flow"))
		w.Header().Set("X-Kratos-Test", "preserved")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":"sent_email"}`)
	}))
	defer upstream.Close()

	limiter, _ := newTestAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	first := postKratosJSON(t, server.URL+"/self-service/login?flow=login-flow", structured.Fields{
		"method":     "code",
		"identifier": "person@example.com",
	})
	require.Equal(t, http.StatusOK, first.StatusCode)
	require.Equal(t, "preserved", first.Header.Get("X-Kratos-Test"))
	require.JSONEq(t, `{"state":"sent_email"}`, readResponseBody(t, first))

	second := postKratosJSON(t, server.URL+"/self-service/login?flow=login-flow", structured.Fields{
		"method":     "code",
		"identifier": "person@example.com",
	})
	require.Equal(t, http.StatusTooManyRequests, second.StatusCode)
	require.Equal(t, "60", second.Header.Get("Retry-After"))
	require.Contains(t, readResponseBody(t, second), "please wait")
	require.EqualValues(t, 1, upstreamCalls.Load())
}

func TestKratosPublicProxyResolvesFlowOnlyResendRecipient(t *testing.T) {
	t.Parallel()
	var postCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/self-service/login/flows":
			require.Equal(t, "login-flow", request.URL.Query().Get("id"))
			require.Contains(t, request.Header.Get("Cookie"), "ory_session=session")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ui":{"nodes":[{"attributes":{"name":"identifier","value":"person@example.com"}}]}}`)
		case request.Method == http.MethodPost && request.URL.Path == "/self-service/login":
			postCalls.Add(1)
			var payload structured.Fields
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			provenance := decodeAuthIssuanceProvenanceForProxyTest(t, payload["transient_payload"].(structured.Fields))
			_, err := VerifyAuthCodeIssuanceProvenance(
				testAuthIssuanceProvenanceKey,
				email.EventLoginCode,
				"person@example.com",
				provenance,
			)
			require.NoError(t, err)
			_, _ = io.WriteString(w, `{"state":"sent_email"}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer upstream.Close()

	limiter, _ := newTestAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/self-service/login?flow=login-flow",
		strings.NewReader(`{"method":"code"}`),
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "ory_session", Value: "session"})
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.EqualValues(t, 1, postCalls.Load())
}

func TestKratosPublicProxyReleasesBudgetWhenKratosRejectsIssuance(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid flow"}}`)
	}))
	defer upstream.Close()

	limits := defaultAuthCodeIssuanceLimits()
	limits.GlobalPerMinute = 1
	limiter, _ := newTestAuthCodeIssuanceLimiter(t, limits)
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	for range 2 {
		response := postKratosJSON(t, server.URL+"/self-service/verification?flow=bad-flow", structured.Fields{
			"method": "code",
			"email":  "person@example.com",
		})
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		_ = readResponseBody(t, response)
	}
	require.EqualValues(t, 2, upstreamCalls.Load())
}

func TestKratosPublicProxyKeepsBudgetForSentEmailStateOnErrorResponse(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ui":{"state":"sent_email"}}`)
	}))
	defer upstream.Close()

	limiter, _ := newTestAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	first := postKratosJSON(t, server.URL+"/self-service/registration?flow=flow", structured.Fields{
		"method": "code",
		"traits": structured.Fields{"email": "person@example.com"},
	})
	require.Equal(t, http.StatusBadRequest, first.StatusCode)
	_ = readResponseBody(t, first)
	second := postKratosJSON(t, server.URL+"/self-service/registration?flow=flow", structured.Fields{
		"method": "code",
		"traits": structured.Fields{"email": "person@example.com"},
	})
	require.Equal(t, http.StatusTooManyRequests, second.StatusCode)
	_ = readResponseBody(t, second)
	require.EqualValues(t, 1, upstreamCalls.Load())
}

func TestKratosPublicProxyDoesNotLimitCodeSubmissionOrOIDC(t *testing.T) {
	t.Parallel()
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	limits := defaultAuthCodeIssuanceLimits()
	limits.GlobalPerMinute = 1
	limiter, _ := newTestAuthCodeIssuanceLimiter(t, limits)
	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	codeResponse := postKratosJSON(t, server.URL+"/self-service/login?flow=flow", structured.Fields{
		"method": "code",
		"code":   "123456",
	})
	require.Equal(t, http.StatusNoContent, codeResponse.StatusCode)
	_ = readResponseBody(t, codeResponse)
	oidcResponse := postKratosJSON(t, server.URL+"/self-service/login?flow=flow", structured.Fields{
		"method":   "oidc",
		"provider": "google",
	})
	require.Equal(t, http.StatusNoContent, oidcResponse.StatusCode)
	_ = readResponseBody(t, oidcResponse)
	require.EqualValues(t, 2, upstreamCalls.Load())
}

func TestInspectAuthCodeIssuanceRequestReadsFormPayload(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://auth.example/self-service/registration?flow=registration-flow",
		bytes.NewBufferString("method=code&traits%5Bemail%5D=person%40example.com"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	issuance, body, candidate, err := inspectAuthCodeIssuanceRequest(request)
	require.NoError(t, err)
	require.True(t, candidate)
	require.Equal(t, "person@example.com", issuance.Recipient)
	require.Equal(t, "registration-flow", issuance.FlowID)
	require.NotEmpty(t, body)
}

func TestInspectAuthCodeIssuanceRequestRecognizesCanonicalPendingEmailSettingsIssuance(t *testing.T) {
	t.Parallel()

	pendingRequest := httptest.NewRequest(
		http.MethodPost,
		"http://auth.example/self-service/settings?flow=settings-flow",
		bytes.NewBufferString(`{"method":"profile","traits":{"email":"old@example.com","pending_email":"new@example.com"}}`),
	)
	pendingRequest.Header.Set("Content-Type", "application/json")
	issuance, _, candidate, err := inspectAuthCodeIssuanceRequest(pendingRequest)
	require.NoError(t, err)
	require.True(t, candidate)
	require.Equal(t, email.EventVerificationCode, issuance.EventKey)
	require.Equal(t, "new@example.com", issuance.Recipient)
	require.Equal(t, "settings-flow", issuance.FlowID)

	legacyAliasRequest := httptest.NewRequest(
		http.MethodPost,
		"http://auth.example/self-service/settings?flow=settings-flow",
		bytes.NewBufferString("method=profile&traits%5Bpending_login_email%5D=login%40example.com"),
	)
	legacyAliasRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, _, candidate, err = inspectAuthCodeIssuanceRequest(legacyAliasRequest)
	require.NoError(t, err)
	require.False(t, candidate)

	ordinaryProfileRequest := httptest.NewRequest(
		http.MethodPost,
		"http://auth.example/self-service/settings?flow=settings-flow",
		bytes.NewBufferString(`{"method":"profile","traits":{"email":"old@example.com","name":"John Doe"}}`),
	)
	ordinaryProfileRequest.Header.Set("Content-Type", "application/json")
	_, _, candidate, err = inspectAuthCodeIssuanceRequest(ordinaryProfileRequest)
	require.NoError(t, err)
	require.False(t, candidate)
}

func TestKratosPublicProxyOverwritesJSONIssuanceProvenanceAndPreservesTransientPayload(t *testing.T) {
	t.Parallel()
	issuedAt := time.Date(2026, time.July, 31, 12, 0, 0, 123, time.UTC)
	limiter := &stubAuthCodeIssuancePreflight{reservation: AuthCodeIssuanceReservation{
		token:    strings.Repeat("a", 64),
		issuedAt: issuedAt,
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload structured.Fields
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		transient := payload["transient_payload"].(structured.Fields)
		require.Equal(t, "ko", transient["locale"])
		require.Equal(t, "preserved", transient["client_value"])
		provenance := decodeAuthIssuanceProvenanceForProxyTest(t, transient)
		require.Equal(t, strings.Repeat("a", 64), provenance.IssuanceID)
		require.Equal(t, issuedAt.Format(time.RFC3339Nano), provenance.IssuedAt)
		_, err := VerifyAuthCodeIssuanceProvenance(
			testAuthIssuanceProvenanceKey,
			email.EventLoginCode,
			"person@example.com",
			provenance,
		)
		require.NoError(t, err)
		_, _ = io.WriteString(w, `{"state":"sent_email"}`)
	}))
	defer upstream.Close()

	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	response := postKratosJSON(t, server.URL+"/self-service/login?flow=flow", structured.Fields{
		"method":     "code",
		"identifier": " Person@Example.COM ",
		"transient_payload": structured.Fields{
			"locale":       "ko",
			"client_value": "preserved",
			AuthCodeIssuanceProvenanceNamespace: structured.Fields{
				"issuance_id": "forged",
				"mac":         "forged",
			},
		},
	})
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = readResponseBody(t, response)
}

func TestKratosPublicProxyOverwritesFormIssuanceProvenanceAndPreservesLocale(t *testing.T) {
	t.Parallel()
	issuedAt := time.Date(2026, time.July, 31, 12, 5, 0, 0, time.UTC)
	limiter := &stubAuthCodeIssuancePreflight{reservation: AuthCodeIssuanceReservation{
		token:    strings.Repeat("b", 64),
		issuedAt: issuedAt,
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		require.Equal(t, "code", values.Get("method"))
		require.Equal(t, "person@example.com", values.Get("traits[email]"))
		var transient structured.Fields
		require.NoError(t, json.Unmarshal([]byte(values.Get("transient_payload")), &transient))
		require.Equal(t, "pt-BR", transient["locale"])
		provenance := decodeAuthIssuanceProvenanceForProxyTest(t, transient)
		_, err = VerifyAuthCodeIssuanceProvenance(
			testAuthIssuanceProvenanceKey,
			email.EventRegistrationCode,
			"person@example.com",
			provenance,
		)
		require.NoError(t, err)
		_, _ = io.WriteString(w, `{"state":"sent_email"}`)
	}))
	defer upstream.Close()

	proxy, err := NewKratosPublicProxy(upstream.URL, limiter, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	server := httptest.NewServer(proxy)
	defer server.Close()

	values := url.Values{
		"method":            {"code"},
		"traits[email]":     {"person@example.com"},
		"transient_payload": {`{"locale":"pt-BR","__geul_auth_issuance":{"mac":"forged"}}`},
	}
	request, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/self-service/registration?flow=flow",
		strings.NewReader(values.Encode()),
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_ = readResponseBody(t, response)
}

func TestAuthCodeClientIPTrustsForwardingOnlyFromPrivateProxy(t *testing.T) {
	t.Parallel()
	publicRemote := httptest.NewRequest(http.MethodGet, "http://auth.example", nil)
	publicRemote.RemoteAddr = "198.51.100.20:443"
	publicRemote.Header.Set("CF-Connecting-IP", "203.0.113.99")
	require.Equal(t, "198.51.100.20", authCodeClientIP(publicRemote))

	privateProxy := httptest.NewRequest(http.MethodGet, "http://auth.example", nil)
	privateProxy.RemoteAddr = "10.0.0.5:40000"
	privateProxy.Header.Set("CF-Connecting-IP", "203.0.113.99")
	require.Equal(t, "203.0.113.99", authCodeClientIP(privateProxy))
}

func TestNewKratosPublicProxyRejectsUnsafeTarget(t *testing.T) {
	t.Parallel()
	limiter := &stubAuthCodeIssuancePreflight{}
	_, err := NewKratosPublicProxy("ftp://kratos.example", limiter, testAuthIssuanceProvenanceKey)
	require.Error(t, err)
	_, err = NewKratosPublicProxy("https://user@kratos.example", limiter, testAuthIssuanceProvenanceKey)
	require.Error(t, err)
	_, err = NewKratosPublicProxy("https://kratos.example", limiter, nil)
	require.Error(t, err)
}

type stubAuthCodeIssuancePreflight struct {
	reservation AuthCodeIssuanceReservation
}

func (s *stubAuthCodeIssuancePreflight) Reserve(
	context.Context,
	AuthCodeIssuanceRequest,
) (AuthCodeIssuanceReservation, bool, time.Duration, error) {
	return s.reservation, true, 0, nil
}

func (*stubAuthCodeIssuancePreflight) Release(context.Context, AuthCodeIssuanceReservation) error {
	return nil
}

func decodeAuthIssuanceProvenanceForProxyTest(
	t *testing.T,
	transient structured.Fields,
) AuthCodeIssuanceProvenance {
	t.Helper()
	reserved, ok := transient[AuthCodeIssuanceProvenanceNamespace]
	require.True(t, ok)
	encoded, err := json.Marshal(reserved)
	require.NoError(t, err)
	var provenance AuthCodeIssuanceProvenance
	require.NoError(t, json.Unmarshal(encoded, &provenance))
	return provenance
}

func postKratosJSON(t *testing.T, target string, body structured.Fields) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	return response
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(body)
}
