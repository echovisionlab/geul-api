//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHookProxyForwardsRegisteredUpstreamAndSerializesControl(t *testing.T) {
	type receivedRequest struct {
		method string
		path   string
		query  string
		header http.Header
		body   string
	}
	received := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
			return
		}
		received <- receivedRequest{
			method: request.Method,
			path:   request.URL.EscapedPath(),
			query:  request.URL.RawQuery,
			header: request.Header.Clone(),
			body:   string(body),
		}
		w.Header().Add("X-Upstream", "one")
		w.Header().Add("X-Upstream", "two")
		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "remove-me")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("forwarded"))
	}))
	defer upstream.Close()

	proxy := startTestHookProxy(t)
	firstRegistration := registerHookProxyOverHTTP(t, proxy, upstream.URL, proxy.ControlToken(), http.StatusNoContent)
	secondRegistration := registerHookProxyOverHTTP(t, proxy, upstream.URL+"/base?fixed=1", proxy.ControlToken(), http.StatusNoContent)
	if firstRegistration == "" || secondRegistration == "" || firstRegistration == secondRegistration {
		t.Fatalf("replacement must return distinct non-empty registrations: first=%q second=%q", firstRegistration, secondRegistration)
	}
	unregisterHookProxyRegistrationOverHTTP(t, proxy, proxy.ControlToken(), firstRegistration, http.StatusNoContent)
	unregisterHookProxyOverHTTP(t, proxy, proxy.ControlToken(), http.StatusBadRequest)

	request, err := http.NewRequest(
		http.MethodPost,
		proxy.LocalBaseURL()+"/hooks/a%2Fb?incoming=2",
		strings.NewReader("payload"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("X-Copied", "first")
	request.Header.Add("X-Copied", "second")
	request.Header.Set("Connection", "X-Request-Hop")
	request.Header.Set("X-Request-Hop", "remove-me")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || string(responseBody) != "forwarded" {
		t.Fatalf("unexpected proxy response: status=%d body=%q", response.StatusCode, responseBody)
	}
	if values := response.Header.Values("X-Upstream"); len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Fatalf("unexpected copied response header: %v", values)
	}
	if response.Header.Get("X-Response-Hop") != "" {
		t.Fatalf("response hop-by-hop header was forwarded: %q", response.Header.Get("X-Response-Hop"))
	}

	forwarded := <-received
	if forwarded.method != http.MethodPost || forwarded.path != "/base/hooks/a%2Fb" {
		t.Fatalf("unexpected forwarded method/path: %s %s", forwarded.method, forwarded.path)
	}
	query, err := url.ParseQuery(forwarded.query)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("fixed") != "1" || query.Get("incoming") != "2" {
		t.Fatalf("unexpected forwarded query: %v", query)
	}
	if forwarded.body != "payload" || forwarded.header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected forwarded body or content type: body=%q content-type=%q", forwarded.body, forwarded.header.Get("Content-Type"))
	}
	if values := forwarded.header.Values("X-Copied"); len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("unexpected copied request header: %v", values)
	}
	if forwarded.header.Get("X-Request-Hop") != "" {
		t.Fatalf("request hop-by-hop header was forwarded: %q", forwarded.header.Get("X-Request-Hop"))
	}

	unregisterHookProxyOverHTTP(t, proxy, "wrong-token", http.StatusUnauthorized)
	unregisterHookProxyRegistrationOverHTTP(t, proxy, proxy.ControlToken(), secondRegistration, http.StatusNoContent)
	requireHookProxyUnavailable(t, proxy.LocalBaseURL()+"/hooks/after-login")
}

func TestHookProxyUnavailableAndControlProtection(t *testing.T) {
	proxy := startTestHookProxy(t)

	requireHookProxyUnavailable(t, proxy.LocalBaseURL()+"/hooks/after-login")
	registerHookProxyOverHTTP(t, proxy, "http://127.0.0.1:1", "wrong-token", http.StatusUnauthorized)
	requireHookProxyUnavailable(t, proxy.LocalBaseURL()+"/hooks/after-login")

	request, err := http.NewRequest(http.MethodGet, proxy.ControlURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+proxy.ControlToken())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != "PUT, DELETE" {
		t.Fatalf("unexpected control method response: status=%d allow=%q", response.StatusCode, response.Header.Get("Allow"))
	}
}

func TestHookProxyURLsAndDirectLifecycle(t *testing.T) {
	proxy, err := StartHookProxy("http://host.testcontainers.internal")
	if err != nil {
		t.Fatal(err)
	}
	local, err := url.Parse(proxy.LocalBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	docker, err := url.Parse(proxy.DockerBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	if local.Hostname() != "127.0.0.1" || docker.Hostname() != "host.testcontainers.internal" || local.Port() != docker.Port() {
		t.Fatalf("unexpected proxy origins: local=%q docker=%q", local, docker)
	}
	if proxy.ControlURL() != proxy.LocalBaseURL()+hookProxyControlPath || len(proxy.ControlToken()) != 64 {
		t.Fatalf("unexpected control coordinates")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	if err := proxy.Register(upstream.URL); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Register(upstream.URL); err != nil {
		t.Fatalf("replace registered upstream: %v", err)
	}
	if err := proxy.Unregister(); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Unregister(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := proxy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := proxy.Register(upstream.URL); err != ErrHookProxyClosed {
		t.Fatalf("expected closed error from register, got %v", err)
	}
}

func TestHookProxyRejectsInvalidOrigins(t *testing.T) {
	for _, raw := range []string{
		"https://host.testcontainers.internal",
		"http://host.testcontainers.internal:8080",
		"http://user@host.testcontainers.internal",
		"http://host.testcontainers.internal/path",
	} {
		if proxy, err := StartHookProxy(raw); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = proxy.Close(ctx)
			cancel()
			t.Fatalf("expected invalid Docker base error for %q", raw)
		}
	}

	proxy := startTestHookProxy(t)
	for _, raw := range []string{
		"",
		"127.0.0.1:8080",
		"file:///tmp/hook",
		"http://user:secret@127.0.0.1:8080",
		"http://127.0.0.1:8080/#fragment",
	} {
		if err := proxy.Register(raw); err == nil {
			t.Fatalf("expected invalid upstream error for %q", raw)
		}
	}
}

func startTestHookProxy(t *testing.T) *HookProxy {
	t.Helper()
	proxy, err := StartHookProxy("http://host.testcontainers.internal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proxy.Close(ctx); err != nil {
			t.Errorf("close hook proxy: %v", err)
		}
	})
	return proxy
}

func registerHookProxyOverHTTP(t *testing.T, proxy *HookProxy, upstream, token string, wantStatus int) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"upstream_base_url": upstream})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, proxy.ControlURL(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected register status: got=%d want=%d body=%q", response.StatusCode, wantStatus, body)
	}
	return response.Header.Get(hookProxyRegistrationHeader)
}

func unregisterHookProxyOverHTTP(t *testing.T, proxy *HookProxy, token string, wantStatus int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, proxy.ControlURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected unregister status: got=%d want=%d body=%q", response.StatusCode, wantStatus, body)
	}
}

func unregisterHookProxyRegistrationOverHTTP(t *testing.T, proxy *HookProxy, token, registration string, wantStatus int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, proxy.ControlURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(hookProxyRegistrationHeader, registration)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected unregister status: got=%d want=%d body=%q", response.StatusCode, wantStatus, body)
	}
}

func requireHookProxyUnavailable(t *testing.T, endpoint string) {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || string(body) != "integration hook upstream unavailable\n" {
		t.Fatalf("unexpected unavailable response: status=%d body=%q", response.StatusCode, body)
	}
}
