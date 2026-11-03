package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

type unifiedAuthFinderStub struct {
	exists        bool
	err           error
	seen          string
	upstreamCalls []string
}

func (f *unifiedAuthFinderStub) FindIdentityByCredentialIdentifier(
	_ context.Context,
	identifier string,
) (*auth.Identity, bool, error) {
	f.seen = identifier
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.exists {
		return nil, false, nil
	}
	return &auth.Identity{ID: "identity-1"}, true, nil
}

func unifiedAuthFlowJSON(id, csrf, email, returnTo string) []byte {
	nodes := []structured.Fields{
		{"type": "input", "group": "default", "attributes": structured.Fields{"name": "csrf_token", "value": csrf}},
	}
	if email != "" {
		nodes = append(nodes,
			structured.Fields{"type": "input", "group": "default", "attributes": structured.Fields{"name": "traits.email", "value": email}},
			structured.Fields{"type": "input", "group": "default", "attributes": structured.Fields{"name": "traits.name", "value": "johndoe"}},
		)
	} else {
		nodes = append(nodes,
			structured.Fields{"type": "input", "group": "default", "attributes": structured.Fields{"name": "identifier", "value": ""}},
		)
	}
	nodes = append(nodes, structured.Fields{"type": "input", "group": "code", "attributes": structured.Fields{"name": "method", "value": "code"}})
	payload := structured.Fields{
		"id": id,
		"ui": structured.Fields{"nodes": nodes},
	}
	if returnTo != "" {
		payload["return_to"] = returnTo
	}
	body, _ := json.Marshal(payload)
	return body
}

func unifiedAuthCodeFlowJSON(id, csrf, email, returnTo string) []byte {
	return unifiedAuthCodeFlowJSONWithMessage(id, csrf, email, returnTo, "info")
}

func unifiedAuthInvalidCodeFlowJSON(id, csrf, email, returnTo string) []byte {
	return unifiedAuthCodeFlowJSONWithMessage(id, csrf, email, returnTo, "error")
}

func unifiedAuthCodeFlowJSONWithMessage(id, csrf, email, returnTo, messageType string) []byte {
	payload := structured.Fields{
		"id":        id,
		"active":    "code",
		"return_to": returnTo,
		"state":     "sent_email",
		"ui": structured.Fields{
			"method":   "POST",
			"messages": []structured.Fields{{"id": 1040005, "type": messageType, "text": "Kratos-private copy"}},
			"nodes": []structured.Fields{
				{"type": "input", "group": "default", "attributes": structured.Fields{"name": "csrf_token", "value": csrf}},
				{"type": "input", "group": "default", "attributes": structured.Fields{"name": "identifier", "value": email}},
				{"type": "input", "group": "code", "attributes": structured.Fields{"name": "code", "value": ""}},
				{"type": "input", "group": "code", "attributes": structured.Fields{"name": "method", "value": "code"}},
				{"type": "input", "group": "code", "attributes": structured.Fields{"name": "resend", "value": "code"}},
			},
		},
	}
	body, _ := json.Marshal(payload)
	return body
}

func unifiedAuthFlowMessageType(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Active string `json:"active"`
		State  string `json:"state"`
		UI     struct {
			Messages []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"messages"`
		} `json:"ui"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, "code", payload.Active)
	require.Equal(t, "sent_email", payload.State)
	require.Len(t, payload.UI.Messages, 1)
	require.Empty(t, payload.UI.Messages[0].Text)
	return payload.UI.Messages[0].Type
}

func mustJSON(t *testing.T, value structured.Value) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}

func newUnifiedAuthTestHandler(
	t *testing.T,
	finder *unifiedAuthFinderStub,
	proxy http.Handler,
) (*UnifiedAuthHandler, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finder.upstreamCalls = append(finder.upstreamCalls, r.Method+" "+r.URL.Path)
		flowID := r.URL.Query().Get("id")
		switch {
		case r.URL.Path == "/self-service/login/flows" && flowID == "login-flow":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("login-flow", "login-csrf", "", "/after-auth"))
		case r.URL.Path == "/self-service/login/flows":
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":{"details":{"redirect_to":"https://auth.example/self-service/login/browser"}}}`))
		case r.URL.Path == "/self-service/registration/flows" && flowID == "registration-flow":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("registration-flow", "registration-csrf", "", "/after-auth"))
		case r.URL.Path == "/self-service/registration/flows" && flowID == "registration-flow-with-email":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("registration-flow-with-email", "registration-csrf", "johndoe@example.com", "/after-auth"))
		case r.URL.Path == "/self-service/registration/browser":
			if got := r.URL.Query().Get("return_to"); got != "/after-auth" {
				t.Fatalf("registration return_to = %q", got)
			}
			http.SetCookie(w, &http.Cookie{
				Name:     "csrf_cookie",
				Value:    "new",
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("registration-flow", "registration-csrf", "", "/after-auth"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	reverseProxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	transport := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet &&
			(strings.HasSuffix(r.URL.Path, "/flows") || r.URL.Path == "/self-service/registration/browser") {
			reverseProxy.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	handler, err := NewUnifiedAuthHandler(transport, finder, RegistrationReuseCheckerFunc(func(context.Context, string) (bool, error) {
		return false, nil
	}))
	if err != nil {
		t.Fatalf("NewUnifiedAuthHandler() error = %v", err)
	}
	handler.reuseHoldCheck = func(context.Context, string) (bool, error) { return false, nil }
	return handler, upstream
}

func TestUnifiedAuthHandlerUsesOnePublicPathForLoginAndRegistration(t *testing.T) {
	t.Run("existing identity stays on the login flow", func(t *testing.T) {
		finder := &unifiedAuthFinderStub{exists: true}
		var forwardedPath string
		var forwardedBody []byte
		proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwardedPath = r.URL.Path
			forwardedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("login-flow", "login-csrf", "", "/after-auth"))
		})
		handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)

		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=login-flow",
			strings.NewReader(`{"method":"code","identifier":"johndoe@example.com","csrf_token":"login-csrf","transient_payload":{"locale":"en"}}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK || forwardedPath != "/self-service/login" {
			t.Fatalf("status = %d, forwarded path = %q", response.Code, forwardedPath)
		}
		if finder.seen != "johndoe@example.com" || !strings.Contains(string(forwardedBody), `"identifier":"johndoe@example.com"`) {
			t.Fatalf("finder = %q, forwarded body = %s", finder.seen, forwardedBody)
		}
	})

	t.Run("first verified email switches internally and exposes the same challenge", func(t *testing.T) {
		finder := &unifiedAuthFinderStub{}
		var forwardedPath string
		var forwardedBody structured.Fields
		var forwardedCookie string
		proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwardedPath = r.URL.Path
			forwardedCookie = r.Header.Get("Cookie")
			if err := json.NewDecoder(r.Body).Decode(&forwardedBody); err != nil {
				t.Fatalf("decode forwarded body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unifiedAuthFlowJSON("registration-flow", "registration-csrf", "johndoe@example.com", "/after-auth"))
		})
		handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)

		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=login-flow",
			strings.NewReader(`{"method":"code","identifier":"johndoe@example.com","csrf_token":"login-csrf","transient_payload":{"display_name":"ignored","bio":"ignored","preferred_locale":"ko"}}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "browser_cookie", Value: "present"})
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK || forwardedPath != "/self-service/registration" {
			t.Fatalf("status = %d, forwarded path = %q, body = %s", response.Code, forwardedPath, response.Body.String())
		}
		if !strings.Contains(forwardedCookie, "browser_cookie=present") || !strings.Contains(forwardedCookie, "csrf_cookie=new") {
			t.Fatalf("forwarded cookie = %q", forwardedCookie)
		}
		if got := forwardedBody["csrf_token"]; got != "registration-csrf" {
			t.Fatalf("csrf_token = %#v", got)
		}
		traits, _ := forwardedBody["traits"].(structured.Fields)
		if traits["email"] != "johndoe@example.com" {
			t.Fatalf("traits = %#v", traits)
		}
		if _, exists := traits["login_emails"]; exists {
			t.Fatalf("registration must not send a second email-code identifier trait: %#v", traits)
		}
		transient, _ := forwardedBody["transient_payload"].(structured.Fields)
		if len(transient) != 1 || transient["preferred_locale"] != "ko" {
			t.Fatalf("transient_payload = %#v", transient)
		}
		if !strings.Contains(strings.Join(response.Header().Values("Set-Cookie"), "\n"), "csrf_cookie=new") {
			t.Fatalf("Set-Cookie = %#v", response.Header().Values("Set-Cookie"))
		}
	})

	t.Run("registration code submission remains behind the unified endpoint", func(t *testing.T) {
		finder := &unifiedAuthFinderStub{}
		var payload structured.Fields
		proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/self-service/registration" {
				t.Fatalf("forwarded path = %q", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session":{"id":"session-1"}}`))
		})
		handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)
		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=registration-flow-with-email",
			strings.NewReader(`{"method":"code","code":"123456","csrf_token":"browser-value","transient_payload":{"preferred_locale":"en"}}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK || payload["code"] != "123456" || payload["csrf_token"] != "registration-csrf" {
			t.Fatalf("status = %d, payload = %#v", response.Code, payload)
		}
	})
}

func TestUnifiedAuthHandlerSinksRetainedDeletedMemberEmailWithoutDelivery(t *testing.T) {
	finder := &unifiedAuthFinderStub{}
	proxyCalled := false
	proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalled = true
		http.Error(w, "unexpected upstream registration", http.StatusInternalServerError)
	})
	handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)
	handler.reuseHoldCheck = func(_ context.Context, address string) (bool, error) {
		require.Equal(t, "former@example.com", email.NormalizeAddressForDelivery(address))
		return true, nil
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"https://auth.example/login?flow=login-flow",
		strings.NewReader(`{"method":"code","identifier":" Former@Example.COM ","csrf_token":"login-csrf"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Empty(t, response.Header().Get("Location"))
	var flow unifiedAuthFlow
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &flow))
	require.Equal(t, "former@example.com", flow.nodeString("identifier"))
	require.Equal(t, "", flow.nodeString("code"))
	require.Equal(t, "info", unifiedAuthFlowMessageType(t, response.Body.Bytes()))
	require.False(t, proxyCalled)
	require.Equal(t, "Former@Example.COM", finder.seen)

	resend := httptest.NewRequest(
		http.MethodPost,
		"https://auth.example/login?flow=registration-flow",
		strings.NewReader(`{"method":"code","identifier":"former@example.com","csrf_token":"registration-csrf"}`),
	)
	resend.Header.Set("Content-Type", "application/json")
	resendResponse := httptest.NewRecorder()
	handler.ServeHTTP(resendResponse, resend)
	require.Equal(t, http.StatusBadRequest, resendResponse.Code)
	require.Equal(t, "info", unifiedAuthFlowMessageType(t, resendResponse.Body.Bytes()))
	require.False(t, proxyCalled, "held-email resend must not reach Kratos courier")

	completion := httptest.NewRequest(
		http.MethodPost,
		"https://auth.example/login?flow=registration-flow",
		strings.NewReader(`{"method":"code","identifier":"former@example.com","code":"123456","csrf_token":"registration-csrf"}`),
	)
	completion.Header.Set("Content-Type", "application/json")
	completionResponse := httptest.NewRecorder()
	handler.ServeHTTP(completionResponse, completion)
	require.Equal(t, http.StatusBadRequest, completionResponse.Code)
	require.Equal(t, "error", unifiedAuthFlowMessageType(t, completionResponse.Body.Bytes()))
	var completionFlow unifiedAuthFlow
	require.NoError(t, json.Unmarshal(completionResponse.Body.Bytes(), &completionFlow))
	require.Equal(t, "former@example.com", completionFlow.nodeString("identifier"))
	require.Equal(t, "", completionFlow.nodeString("code"))
	require.False(t, proxyCalled, "held-email completion must not reach Kratos")
}

func TestUnifiedAuthHandlerChecksAuthoritativeRegistrationEmailWithoutRequestIdentifier(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "resend", body: `{"method":"code","csrf_token":"registration-csrf"}`},
		{name: "completion", body: `{"method":"code","code":"123456","csrf_token":"registration-csrf"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finder := &unifiedAuthFinderStub{}
			proxyCalled := false
			handler, _ := newUnifiedAuthTestHandler(t, finder, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				proxyCalled = true
			}))
			handler.reuseHoldCheck = func(_ context.Context, email string) (bool, error) {
				require.Equal(t, "johndoe@example.com", email)
				return true, nil
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"https://auth.example/login?flow=registration-flow-with-email",
				strings.NewReader(tt.body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusBadRequest, response.Code)
			require.False(t, proxyCalled)
		})
	}
}

func TestUnifiedAuthHandlerDoesNotLetRequestIdentifierReplaceAuthoritativeRegistrationEmail(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "resend"},
		{name: "completion", code: "123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finder := &unifiedAuthFinderStub{}
			var forwarded structured.Fields
			handler, _ := newUnifiedAuthTestHandler(t, finder, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&forwarded))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"registration-flow-with-email","ui":{"nodes":[]}}`))
			}))
			handler.reuseHoldCheck = func(_ context.Context, email string) (bool, error) {
				require.Equal(t, "johndoe@example.com", email)
				return false, nil
			}
			payload := structured.Fields{
				"method":     "code",
				"identifier": "former@example.com",
				"csrf_token": "registration-csrf",
			}
			if tt.code != "" {
				payload["code"] = tt.code
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			request := httptest.NewRequest(
				http.MethodPost,
				"https://auth.example/login?flow=registration-flow-with-email",
				bytes.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			traits, ok := forwarded["traits"].(structured.Fields)
			require.True(t, ok)
			require.Equal(t, "johndoe@example.com", traits["email"])
			require.NotContains(t, string(body), "johndoe@example.com")
			forwardedJSON, err := json.Marshal(forwarded)
			require.NoError(t, err)
			require.NotContains(t, string(forwardedJSON), "former@example.com")
		})
	}
}

func TestUnifiedAuthHandlerForwardsRegistrationCodeResend(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantResend bool
	}{
		{name: "resend", body: `{"method":"code","resend":"code","csrf_token":"registration-csrf"}`, wantResend: true},
		{name: "completion keeps the code", body: `{"method":"code","resend":"code","code":"123456","csrf_token":"registration-csrf"}`},
		{name: "no resend requested", body: `{"method":"code","csrf_token":"registration-csrf"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var forwarded structured.Fields
			handler, _ := newUnifiedAuthTestHandler(t, &unifiedAuthFinderStub{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&forwarded))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"registration-flow-with-email","ui":{"nodes":[]}}`))
			}))
			handler.reuseHoldCheck = func(context.Context, string) (bool, error) { return false, nil }
			request := httptest.NewRequest(
				http.MethodPost,
				"https://auth.example/login?flow=registration-flow-with-email",
				strings.NewReader(tt.body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			if tt.wantResend {
				require.Equal(t, "code", forwarded["resend"])
				require.NotContains(t, forwarded, "code")
			} else {
				require.NotContains(t, forwarded, "resend")
			}
		})
	}
}

func TestUnifiedAuthHandlerRejectsRegistrationCodeWithoutAnyEmailCandidate(t *testing.T) {
	finder := &unifiedAuthFinderStub{}
	proxyCalled := false
	handler, _ := newUnifiedAuthTestHandler(t, finder, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyCalled = true
	}))
	handler.reuseHoldCheck = func(context.Context, string) (bool, error) {
		t.Fatal("reuse hold must not be queried without an email candidate")
		return false, nil
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"https://auth.example/login?flow=registration-flow",
		strings.NewReader(`{"method":"code","code":"123456","csrf_token":"registration-csrf"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.False(t, proxyCalled)
}

func TestUnifiedAuthCodeChallengeHasSamePublicResponseShapeForKnownAndNewEmail(t *testing.T) {
	type publicResponse struct {
		status        int
		header        http.Header
		body          structured.Fields
		cookieShape   []string
		cookieValue   string
		upstreamCalls []string
		selectedPosts int
	}

	issueChallenge := func(t *testing.T, exists, blocked bool) publicResponse {
		t.Helper()
		flowKind := "registration"
		flowID := "registration-flow"
		csrf := "registration-csrf"
		if exists {
			flowKind = "login"
			flowID = "login-flow"
			csrf = "login-csrf"
		}
		proxyCalls := 0
		proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			proxyCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Kratos-Flow-Kind", flowKind)
			http.SetCookie(w, &http.Cookie{
				Name:     "csrf_cookie",
				Value:    flowKind + "-final",
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(unifiedAuthCodeFlowJSON(flowID, csrf, "johndoe@example.com", "/after-auth"))
		})
		finder := &unifiedAuthFinderStub{exists: exists}
		handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)
		handler.reuseHoldCheck = func(context.Context, string) (bool, error) { return blocked, nil }
		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=login-flow&refresh=true",
			strings.NewReader(`{"method":"code","identifier":" JohnDoe@Example.COM ","csrf_token":"login-csrf","refresh":true}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		var body structured.Fields
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		body["id"] = "opaque-flow"
		for _, rawNode := range body["ui"].(structured.Fields)["nodes"].(structured.Values) {
			attributes := rawNode.(structured.Fields)["attributes"].(structured.Fields)
			if attributes["name"] == "csrf_token" {
				attributes["value"] = "opaque-csrf"
			}
		}
		cookies := response.Result().Cookies()
		cookieShape := make([]string, 0, len(cookies))
		for _, cookie := range cookies {
			cookieShape = append(cookieShape, strings.Join([]string{
				cookie.Name,
				cookie.Path,
				cookie.Domain,
				strconv.Itoa(int(cookie.SameSite)),
				strconv.FormatBool(cookie.Secure),
				strconv.FormatBool(cookie.HttpOnly),
			}, "|"))
		}
		cookieValue := ""
		if len(cookies) == 1 {
			cookieValue = cookies[0].Value
		}
		publicHeader := response.Header().Clone()
		publicHeader.Del("Set-Cookie")
		upstreamCalls := append([]string(nil), finder.upstreamCalls...)
		for range proxyCalls {
			upstreamCalls = append(upstreamCalls, "POST selected flow")
		}
		return publicResponse{
			status:        response.Code,
			header:        publicHeader,
			body:          body,
			cookieShape:   cookieShape,
			cookieValue:   cookieValue,
			upstreamCalls: upstreamCalls,
			selectedPosts: proxyCalls,
		}
	}

	known := issueChallenge(t, true, false)
	newEmail := issueChallenge(t, false, false)
	heldEmail := issueChallenge(t, false, true)
	require.Equal(t, known.status, newEmail.status)
	require.Equal(t, http.StatusBadRequest, known.status)
	require.Equal(t, known.header, newEmail.header)
	require.Equal(t, known.cookieShape, newEmail.cookieShape)
	require.Equal(t, known.body, newEmail.body)
	require.Equal(t, known.upstreamCalls, newEmail.upstreamCalls)
	require.Equal(t, known.status, heldEmail.status)
	require.Equal(t, known.header, heldEmail.header)
	require.Equal(t, known.cookieShape, heldEmail.cookieShape)
	require.Equal(t, known.body, heldEmail.body)
	for _, rawNode := range known.body["ui"].(structured.Fields)["nodes"].(structured.Values) {
		attributes := rawNode.(structured.Fields)["attributes"].(structured.Fields)
		switch attributes["name"] {
		case "identifier":
			require.Equal(t, "johndoe@example.com", attributes["value"])
		case "code":
			require.Equal(t, "", attributes["value"])
		case "method", "resend":
			require.Equal(t, "code", attributes["value"])
		}
	}
	require.Equal(t, []string{
		"GET /self-service/login/flows",
		"GET /self-service/registration/browser",
		"POST selected flow",
	}, known.upstreamCalls)
	require.Equal(t, []string{
		"GET /self-service/login/flows",
		"GET /self-service/registration/browser",
	}, heldEmail.upstreamCalls)
	require.Equal(t, 1, known.selectedPosts)
	require.Equal(t, 1, newEmail.selectedPosts)
	require.Zero(t, heldEmail.selectedPosts)
	require.Equal(t, "registration-final", newEmail.cookieValue, "registration bootstrap cookie must be replaced, not duplicated")
	require.Empty(t, known.header.Get("Location"))
	require.Equal(t, "no-store", known.header.Get("Cache-Control"))
	require.Empty(t, known.header.Get("X-Kratos-Flow-Kind"))
	require.Equal(t, "info", unifiedAuthFlowMessageType(t, mustJSON(t, known.body)))
}

func TestUnifiedAuthHeldRegistrationFollowupsMatchKratosCodeFlowSurface(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		messageType string
	}{
		{
			name:        "resend",
			body:        `{"method":"code","resend":"code","csrf_token":"registration-csrf"}`,
			messageType: "info",
		},
		{
			name:        "invalid code",
			body:        `{"method":"code","code":"000000","csrf_token":"registration-csrf"}`,
			messageType: "error",
		},
	}

	type publicResponse struct {
		status      int
		header      http.Header
		body        structured.Fields
		cookies     []string
		proxyCalls  int
		publicBytes []byte
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			submit := func(t *testing.T, blocked bool) publicResponse {
				t.Helper()
				proxyCalls := 0
				proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					proxyCalls++
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Kratos-Flow-Kind", "registration")
					w.WriteHeader(http.StatusBadRequest)
					if tt.messageType == "error" {
						_, _ = w.Write(unifiedAuthInvalidCodeFlowJSON(
							"registration-flow-with-email",
							"registration-csrf",
							"johndoe@example.com",
							"/after-auth",
						))
						return
					}
					_, _ = w.Write(unifiedAuthCodeFlowJSON(
						"registration-flow-with-email",
						"registration-csrf",
						"johndoe@example.com",
						"/after-auth",
					))
				})
				handler, _ := newUnifiedAuthTestHandler(t, &unifiedAuthFinderStub{}, proxy)
				handler.reuseHoldCheck = func(context.Context, string) (bool, error) { return blocked, nil }
				request := httptest.NewRequest(
					http.MethodPost,
					"https://auth.example/login?flow=registration-flow-with-email",
					strings.NewReader(tt.body),
				)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()

				handler.ServeHTTP(response, request)

				var body structured.Fields
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				body["id"] = "opaque-flow"
				for _, rawNode := range body["ui"].(structured.Fields)["nodes"].(structured.Values) {
					attributes := rawNode.(structured.Fields)["attributes"].(structured.Fields)
					if attributes["name"] == "csrf_token" {
						attributes["value"] = "opaque-csrf"
					}
				}
				header := response.Header().Clone()
				header.Del("Set-Cookie")
				cookies := make([]string, 0, len(response.Result().Cookies()))
				for _, cookie := range response.Result().Cookies() {
					cookies = append(cookies, strings.Join([]string{
						cookie.Name,
						cookie.Path,
						cookie.Domain,
						strconv.Itoa(int(cookie.SameSite)),
						strconv.FormatBool(cookie.Secure),
						strconv.FormatBool(cookie.HttpOnly),
					}, "|"))
				}
				return publicResponse{
					status:      response.Code,
					header:      header,
					body:        body,
					cookies:     cookies,
					proxyCalls:  proxyCalls,
					publicBytes: response.Body.Bytes(),
				}
			}

			normal := submit(t, false)
			held := submit(t, true)
			require.Equal(t, http.StatusBadRequest, normal.status)
			require.Equal(t, normal.status, held.status)
			require.Equal(t, normal.header, held.header)
			require.Equal(t, normal.cookies, held.cookies)
			require.Equal(t, normal.body, held.body)
			require.Equal(t, 1, normal.proxyCalls)
			require.Zero(t, held.proxyCalls)
			require.Equal(t, tt.messageType, unifiedAuthFlowMessageType(t, normal.publicBytes))
			require.Equal(t, tt.messageType, unifiedAuthFlowMessageType(t, held.publicBytes))
			require.Empty(t, normal.header.Get("X-Kratos-Flow-Kind"))
			require.Empty(t, normal.header.Get("Location"))
		})
	}
}

func TestUnifiedAuthHandlerPreservesRetryAfterAndFailsClosed(t *testing.T) {
	t.Run("preserves Retry-After from the shared public proxy", func(t *testing.T) {
		finder := &unifiedAuthFinderStub{exists: true}
		proxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "47")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		handler, _ := newUnifiedAuthTestHandler(t, finder, proxy)
		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=login-flow",
			strings.NewReader(`{"method":"code","identifier":"johndoe@example.com"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "47" {
			t.Fatalf("status = %d, Retry-After = %q", response.Code, response.Header().Get("Retry-After"))
		}
	})

	t.Run("identity lookup failure never falls through to registration", func(t *testing.T) {
		finder := &unifiedAuthFinderStub{err: errors.New("admin unavailable")}
		proxyCalls := 0
		handler, _ := newUnifiedAuthTestHandler(t, finder, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			proxyCalls++
		}))
		request := httptest.NewRequest(
			http.MethodPost,
			"https://auth.example/login?flow=login-flow",
			strings.NewReader(`{"method":"code","identifier":"johndoe@example.com"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusServiceUnavailable || proxyCalls != 0 {
			t.Fatalf("status = %d, proxy calls = %d", response.Code, proxyCalls)
		}
	})
}

func TestUnifiedAuthHandlerRefreshCodeWithExistingSession(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/x-www-form-urlencoded"} {
		t.Run(contentType, func(t *testing.T) {
			const returnTo = "https://app.example/settings?tab=security"
			finder := &unifiedAuthFinderStub{exists: true}
			var registrationCalls, loginPosts, deliveries int
			var expectedBody []byte
			activeCode := ""
			flowBody := func(body []byte) []byte {
				var flow structured.Fields
				require.NoError(t, json.Unmarshal(body, &flow))
				flow["refresh"] = true
				return mustJSON(t, flow)
			}
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				require.Equal(t, "ory_kratos_session=valid-old-session; csrf_cookie=login", r.Header.Get("Cookie"))
				switch r.Method + " " + r.URL.Path {
				case "GET /self-service/login/flows":
					require.Equal(t, url.Values{"id": {"refresh-flow"}}, r.URL.Query())
					if activeCode == "" {
						_, _ = w.Write(flowBody(unifiedAuthFlowJSON("refresh-flow", "login-csrf", "", returnTo)))
					} else {
						_, _ = w.Write(flowBody(unifiedAuthCodeFlowJSON("refresh-flow", "login-csrf", "person@example.com", returnTo)))
					}
				case "GET /self-service/registration/browser":
					registrationCalls++
					// Kratos refuses registration while this browser has a valid session.
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"id":"session_already_available"}}`)
				case "POST /self-service/login":
					loginPosts++
					require.Equal(t, url.Values{"flow": {"refresh-flow"}}, r.URL.Query())
					require.Equal(t, contentType, r.Header.Get("Content-Type"))
					require.Equal(t, "https://app.example", r.Header.Get("Origin"))
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, expectedBody, body, "login must retain the submitted CSRF, code, and resend fields")
					fields := make(map[string]string)
					if contentType == "application/json" {
						require.NoError(t, json.Unmarshal(body, &fields))
					} else {
						values, err := url.ParseQuery(string(body))
						require.NoError(t, err)
						for name := range values {
							fields[name] = values.Get(name)
						}
					}
					if fields["csrf_token"] != "login-csrf" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"error":{"id":"security_csrf_violation"}}`)
						return
					}
					if fields["code"] != "" {
						if activeCode == "" || fields["code"] != activeCode {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = w.Write(flowBody(unifiedAuthInvalidCodeFlowJSON("refresh-flow", "login-csrf", "person@example.com", returnTo)))
							return
						}
						http.SetCookie(w, &http.Cookie{Name: "ory_kratos_session", Value: "refreshed-session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
						w.Header().Set("Location", returnTo)
						_, _ = io.WriteString(w, `{"session":{"id":"refreshed-session"}}`)
						return
					}
					if activeCode == "" || fields["resend"] == "code" {
						deliveries++
						activeCode = strconv.Itoa(123455 + deliveries)
					}
					http.SetCookie(w, &http.Cookie{Name: "csrf_cookie", Value: "login", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write(flowBody(unifiedAuthCodeFlowJSON("refresh-flow", "login-csrf", "person@example.com", returnTo)))
				default:
					t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL)
				}
			})
			handler, err := NewUnifiedAuthHandler(upstream, finder, RegistrationReuseCheckerFunc(func(context.Context, string) (bool, error) {
				t.Fatal("refresh must not consult registration holds")
				return false, nil
			}))
			require.NoError(t, err)
			submit := func(csrf, code, resend string) *httptest.ResponseRecorder {
				t.Helper()
				fields := map[string]string{"method": "code", "identifier": "person@example.com", "csrf_token": csrf, "refresh": "false"}
				if code != "" {
					fields["code"] = code
				}
				if resend != "" {
					fields["resend"] = resend
				}
				if contentType == "application/json" {
					expectedBody = mustJSON(t, fields)
				} else {
					values := make(url.Values)
					for name, value := range fields {
						values.Set(name, value)
					}
					expectedBody = []byte(values.Encode())
				}
				request := httptest.NewRequest(http.MethodPost, "https://auth.example/login?flow=refresh-flow&refresh=false&return_to=https://attacker.example", bytes.NewReader(expectedBody))
				request.Header.Set("Content-Type", contentType)
				request.Header.Set("Origin", "https://app.example")
				request.Header.Set("Cookie", "ory_kratos_session=valid-old-session; csrf_cookie=login")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			assertChallenge := func(response *httptest.ResponseRecorder, messageType string) {
				t.Helper()
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				require.Equal(t, messageType, unifiedAuthFlowMessageType(t, response.Body.Bytes()))
				flow, err := decodeUnifiedAuthFlow(response.Body.Bytes())
				require.NoError(t, err)
				require.Equal(t, "refresh-flow", flow.ID)
				require.True(t, flow.Refresh)
				require.Equal(t, returnTo, flow.ReturnTo)
				require.Equal(t, "login-csrf", flow.nodeString("csrf_token"))
				require.NotContains(t, response.Body.String(), `"session"`)
				require.Empty(t, response.Header().Get("Location"))
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				for _, cookie := range response.Result().Cookies() {
					require.NotEqual(t, "ory_kratos_session", cookie.Name, "old session alone must not complete refresh")
				}
			}

			initial := submit("login-csrf", "", "")
			require.Equal(t, 1, deliveries, "refresh must reach login code issuance; registration calls=%d, response=%s", registrationCalls, initial.Body.String())
			assertChallenge(initial, "info")
			require.Equal(t, []string{"csrf_cookie=login; Path=/; HttpOnly; Secure; SameSite=Lax"}, initial.Header().Values("Set-Cookie"))

			for _, fields := range [][2]string{{"", ""}, {"forged-csrf", ""}, {"forged-csrf", "123456"}} {
				response := submit(fields[0], fields[1], "code")
				require.Equal(t, http.StatusForbidden, response.Code)
				require.JSONEq(t, `{"error":{"id":"security_csrf_violation"}}`, response.Body.String())
				require.Empty(t, response.Result().Cookies())
				require.Equal(t, 1, deliveries)
			}
			assertChallenge(submit("login-csrf", "000000", ""), "error")
			require.Equal(t, 1, deliveries)
			assertChallenge(submit("login-csrf", "", "code"), "info")
			require.Equal(t, 2, deliveries, "resend must issue through the same refresh login flow")
			assertChallenge(submit("login-csrf", "123456", ""), "error")
			completed := submit("login-csrf", "123457", "")
			require.Equal(t, http.StatusOK, completed.Code)
			require.JSONEq(t, `{"session":{"id":"refreshed-session"}}`, completed.Body.String())
			require.Equal(t, returnTo, completed.Header().Get("Location"))
			require.Len(t, completed.Result().Cookies(), 1)
			require.Equal(t, "refreshed-session", completed.Result().Cookies()[0].Value)
			require.Equal(t, 2, deliveries)
			require.Equal(t, 8, loginPosts)
			require.Zero(t, registrationCalls, "authenticated refresh must never bootstrap registration")
			require.Empty(t, finder.seen, "Kratos login remains the identity authority for refresh")
		})
	}
}

func TestUnifiedAuthHandlerRefreshUnknownIdentifierNeverRegisters(t *testing.T) {
	for _, body := range []string{
		`{"method":"code","identifier":"unknown@example.com","csrf_token":"login-csrf"}`,
		`{"method":"code","identifier":"unknown@example.com","csrf_token":"login-csrf","resend":"code"}`,
		`{"method":"code","identifier":"unknown@example.com","csrf_token":"login-csrf","code":"123456"}`,
	} {
		t.Run(body, func(t *testing.T) {
			finder := &unifiedAuthFinderStub{}
			var calls []string
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /self-service/login/flows":
					var flow structured.Fields
					require.NoError(t, json.Unmarshal(unifiedAuthFlowJSON("refresh-flow", "login-csrf", "", "/after-auth"), &flow))
					flow["refresh"] = true
					_, _ = w.Write(mustJSON(t, flow))
				case "GET /self-service/registration/browser":
					// Without a session, registration would succeed and could create an account.
					_, _ = w.Write(unifiedAuthFlowJSON("registration-flow", "registration-csrf", "", "/after-auth"))
				case "POST /self-service/login":
					require.Equal(t, "refresh-flow", r.URL.Query().Get("flow"))
					forwarded, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, body, string(forwarded))
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"id":"invalid_credentials"}}`)
				default:
					t.Fatalf("refresh reached an unexpected endpoint: %s %s", r.Method, r.URL)
				}
			})
			handler, err := NewUnifiedAuthHandler(upstream, finder, RegistrationReuseCheckerFunc(func(context.Context, string) (bool, error) {
				t.Fatal("refresh must not make a registration eligibility decision")
				return false, nil
			}))
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "https://auth.example/login?flow=refresh-flow", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.JSONEq(t, `{"error":{"id":"invalid_credentials"}}`, response.Body.String())
			require.Empty(t, response.Result().Cookies())
			require.Equal(t, []string{"GET /self-service/login/flows", "POST /self-service/login"}, calls)
			require.Empty(t, finder.seen)
		})
	}
}

func TestProjectUnifiedAuthFlowRemovesInternalRegistrationSignals(t *testing.T) {
	body := []byte(`{
		"id":"018f0000-0000-7000-8000-000000000001",
		"request_url":"https://identity.example/self-service/registration/browser",
		"identity_schema":{"id":"user"},
		"transient_payload":{"__geul_auth_issuance":{"recipient":"johndoe@example.com"}},
		"ui":{
			"action":"https://identity.example/self-service/registration?flow=018f0000-0000-7000-8000-000000000001",
			"messages":[{"id":1,"type":"info","text":"Registration code sent"}],
			"nodes":[
				{"attributes":{"name":"traits.email","value":"johndoe@example.com"},"messages":[]},
				{"attributes":{"name":"traits.name","value":"johndoe"},"messages":[]},
				{"attributes":{"name":"code","value":""},"messages":[{"type":"error","text":"Registration code invalid"}]}
			]
		}
	}`)

	projected := projectUnifiedAuthFlow(body)
	var payload structured.Fields
	if err := json.Unmarshal(projected, &payload); err != nil {
		t.Fatalf("decode projection: %v", err)
	}
	if _, present := payload["request_url"]; present {
		t.Fatal("request_url leaked internal flow kind")
	}
	if _, present := payload["identity_schema"]; present {
		t.Fatal("identity_schema leaked internal registration state")
	}
	if _, present := payload["transient_payload"]; present {
		t.Fatal("transient auth issuance metadata leaked to the browser")
	}
	ui := payload["ui"].(structured.Fields)
	if _, present := ui["action"]; present {
		t.Fatal("Kratos registration action leaked to the browser")
	}
	nodes := ui["nodes"].(structured.Values)
	if len(nodes) != 5 {
		t.Fatalf("projected nodes = %#v", nodes)
	}
	identifierAttributes := nodes[1].(structured.Fields)["attributes"].(structured.Fields)
	if identifierAttributes["name"] != "identifier" {
		t.Fatalf("email node name = %#v", identifierAttributes["name"])
	}
	encoded := string(projected)
	for _, forbidden := range []string{"registration", "Registration", "traits.name"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectUnifiedAuthCodeFlowHasSameLoginAndRegistrationSchema(t *testing.T) {
	t.Parallel()
	login := []byte(`{
		"id":"login-flow","type":"browser","request_url":"https://identity.test/self-service/login",
		"ui":{"action":"https://identity.test/self-service/login?flow=login-flow","messages":[{"id":101,"type":"info","text":"login code sent","context":{"recipient":"known@example.test"}}],"nodes":[
			{"type":"input","group":"default","attributes":{"name":"csrf_token","type":"hidden","value":"login-csrf"},"messages":[]},
			{"type":"input","group":"default","attributes":{"name":"identifier","type":"email","value":"person@example.test"},"messages":[]},
			{"type":"input","group":"code","attributes":{"name":"code","type":"text","value":""},"messages":[]}
		]}}
	`)
	registration := []byte(`{
		"id":"registration-flow","type":"browser","request_url":"https://identity.test/self-service/registration",
		"identity_schema":{"id":"user"},
		"ui":{"action":"https://identity.test/self-service/registration?flow=registration-flow","messages":[{"id":202,"type":"info","text":"registration code sent","context":{"recipient":"unknown@example.test"}}],"nodes":[
			{"type":"input","group":"default","attributes":{"name":"csrf_token","type":"hidden","value":"registration-csrf"},"messages":[]},
			{"type":"input","group":"default","attributes":{"name":"traits.email","type":"email","value":"person@example.test"},"messages":[]},
			{"type":"input","group":"default","attributes":{"name":"traits.name","type":"text","value":"johndoe"},"messages":[]},
			{"type":"input","group":"code","attributes":{"name":"code","type":"text","value":""},"messages":[]}
		]}}
	`)

	normalize := func(body []byte) structured.Fields {
		var payload structured.Fields
		require.NoError(t, json.Unmarshal(projectUnifiedAuthFlow(body), &payload))
		payload["id"] = "flow"
		for _, rawNode := range payload["ui"].(structured.Fields)["nodes"].(structured.Values) {
			rawNode.(structured.Fields)["attributes"].(structured.Fields)["value"] = "value"
		}
		return payload
	}
	require.Equal(t, normalize(login), normalize(registration))
	encoded, err := json.Marshal(normalize(registration))
	require.NoError(t, err)
	for _, forbidden := range []string{"registration", "identity_schema", "recipient", `"id":202`} {
		require.NotContains(t, string(encoded), forbidden)
	}
}
