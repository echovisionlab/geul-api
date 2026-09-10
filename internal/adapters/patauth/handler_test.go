package patauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/member/pat"
)

const testSecret = "gateway-secret-for-pat-tests-only"

type authenticateFunc func(context.Context, string) (pat.Principal, error)

func (f authenticateFunc) Authenticate(ctx context.Context, token string) (pat.Principal, error) {
	return f(ctx, token)
}

func authenticatedRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, Path, nil)
	r.SetBasicAuth(GatewayUsername, testSecret)
	r.Header.Set(APIKeyHeader, "test-bearer")
	return r
}

func TestTransportAndAuthenticationFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*http.Request)
		principal pat.Principal
		err       error
		want      int
		calls     int
	}{
		{name: "success", principal: pat.Principal{MemberID: "member", TokenID: "selector"}, want: 200, calls: 1},
		{name: "wrong path", mutate: func(r *http.Request) { r.URL.Path += "/" }, want: 404},
		{name: "method", mutate: func(r *http.Request) { r.Method = http.MethodGet }, want: 405},
		{name: "missing gateway", mutate: func(r *http.Request) { r.Header.Del("Authorization") }, want: 401},
		{name: "wrong password", mutate: func(r *http.Request) { r.SetBasicAuth(GatewayUsername, "bad") }, want: 401},
		{name: "wrong user", mutate: func(r *http.Request) { r.SetBasicAuth("other", testSecret) }, want: 401},
		{name: "duplicate gateway", mutate: func(r *http.Request) { r.Header.Add("Authorization", r.Header.Get("Authorization")) }, want: 401},
		{name: "missing PAT", mutate: func(r *http.Request) { r.Header.Del(APIKeyHeader) }, want: 400},
		{name: "duplicate PAT", mutate: func(r *http.Request) { r.Header.Add(APIKeyHeader, "another") }, want: 400},
		{name: "large PAT", mutate: func(r *http.Request) { r.Header.Set(APIKeyHeader, strings.Repeat("x", 129)) }, want: 400},
		{name: "query", mutate: func(r *http.Request) { r.URL.RawQuery = "token=secret" }, want: 400},
		{name: "body", mutate: func(r *http.Request) { r.Body = http.NoBody; r.Body = &testBody{value: "x"} }, want: 400},
		{name: "large body", mutate: func(r *http.Request) { r.Body = &testBody{value: "xx"} }, want: 400},
		{name: "read error", mutate: func(r *http.Request) { r.Body = &testBody{err: errors.New("read")} }, want: 400},
		{name: "invalid PAT", err: pat.ErrInvalidToken, want: 401, calls: 1},
		{name: "database failed", err: errors.New("private DB error"), want: 503, calls: 1},
		{name: "invalid stored PAT", err: pat.ErrInvalidStoredToken, want: 503, calls: 1},
		{name: "empty principal", want: 503, calls: 1},
		{name: "empty token id", principal: pat.Principal{MemberID: "member"}, want: 503, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := 0
			h, err := New(testSecret, authenticateFunc(func(ctx context.Context, token string) (pat.Principal, error) {
				called++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > authenticationTimeout {
					t.Error("missing bounded deadline")
				}
				if token != "test-bearer" {
					t.Error("unexpected token")
				}
				return tc.principal, tc.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := authenticatedRequest()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != tc.calls {
				t.Fatalf("status %d calls %d, want %d/%d", w.Code, called, tc.want, tc.calls)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Error("cacheable auth response")
			}
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "test-bearer") {
				t.Error("secret leaked")
			}
		})
	}
}

type testBody struct {
	value string
	err   error
}

func (b *testBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.value == "" {
		return 0, io.EOF
	}
	n := copy(p, b.value)
	b.value = b.value[n:]
	return n, nil
}
func (*testBody) Close() error { return nil }

func TestConstructorRejectsMissingDependencies(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("x", 31), strings.Repeat("x", 257)} {
		if _, err := New(s, authenticateFunc(nil)); err == nil {
			t.Error("accepted invalid secret")
		}
	}
	if _, err := New(testSecret, nil); err == nil {
		t.Error("accepted missing authenticator")
	}
}

func TestSaturationAndCancellation(t *testing.T) {
	entered := make(chan struct{}, maximumConcurrentChecks)
	h, err := New(testSecret, authenticateFunc(func(ctx context.Context, _ string) (pat.Principal, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return pat.Principal{}, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	for range maximumConcurrentChecks {
		wg.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, authenticatedRequest().WithContext(ctx))
			if w.Code != 503 {
				t.Errorf("cancel status %d", w.Code)
			}
		})
	}
	for range maximumConcurrentChecks {
		<-entered
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authenticatedRequest())
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturation status %d", w.Code)
	}
	cancel()
	wg.Wait()
	if len(h.slots) != 0 {
		t.Error("slot leak")
	}
}
