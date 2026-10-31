package patauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

const oathkeeperTestHeader = "X-Test-Internal-Service"
const oathkeeperTestToken = "pat_selector.private-secret"

func oathkeeperRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, OathkeeperPath, nil)
	r.Header.Set(oathkeeperTestHeader, testSecret)
	r.Header.Set("Authorization", "Bearer "+oathkeeperTestToken)
	return r
}

func TestOathkeeperTransportAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*http.Request)
		principal pat.Principal
		err       error
		want      int
		calls     int
	}{
		{name: "success", principal: pat.Principal{MemberID: "member", TokenID: "selector"}, want: 200, calls: 1},
		{name: "case insensitive scheme", mutate: func(r *http.Request) { r.Header.Set("Authorization", "bearer "+oathkeeperTestToken) }, principal: pat.Principal{MemberID: "member", TokenID: "selector"}, want: 200, calls: 1},
		{name: "wrong path", mutate: func(r *http.Request) { r.URL.Path += "/" }, want: 404},
		{name: "wrong method", mutate: func(r *http.Request) { r.Method = http.MethodPost }, want: 405},
		{name: "missing caller", mutate: func(r *http.Request) { r.Header.Del(oathkeeperTestHeader) }, want: 401},
		{name: "wrong caller", mutate: func(r *http.Request) { r.Header.Set(oathkeeperTestHeader, "wrong") }, want: 401},
		{name: "duplicate caller", mutate: func(r *http.Request) { r.Header.Add(oathkeeperTestHeader, testSecret) }, want: 401},
		{name: "missing bearer", mutate: func(r *http.Request) { r.Header.Del("Authorization") }, want: 401},
		{name: "basic", mutate: func(r *http.Request) { r.SetBasicAuth(GatewayUsername, testSecret) }, want: 401},
		{name: "duplicate bearer", mutate: func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+oathkeeperTestToken) }, want: 401},
		{name: "combined bearer", mutate: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+oathkeeperTestToken+", Bearer pat_other")
		}, want: 401},
		{name: "wrong prefix", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer oauth_token") }, want: 401},
		{name: "empty token", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, want: 401},
		{name: "extra whitespace", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer  "+oathkeeperTestToken) }, want: 401},
		{name: "large bearer", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer pat_"+strings.Repeat("x", 129)) }, want: 401},
		{name: "query", mutate: func(r *http.Request) { r.URL.RawQuery = "token=private" }, want: 401},
		{name: "body", mutate: func(r *http.Request) { r.Body = &testBody{value: "x"} }, want: 401},
		{name: "read error", mutate: func(r *http.Request) { r.Body = &testBody{err: errors.New("read")} }, want: 401},
		{name: "invalid PAT", err: pat.ErrInvalidToken, want: 401, calls: 1},
		{name: "wrapped invalid PAT", err: fmt.Errorf("verify: %w", pat.ErrInvalidToken), want: 401, calls: 1},
		{name: "database failure", err: errors.New("private DB error"), want: 503, calls: 1},
		{name: "invalid stored PAT", err: pat.ErrInvalidStoredToken, want: 503, calls: 1},
		{name: "empty principal", want: 503, calls: 1},
		{name: "empty token id", principal: pat.Principal{MemberID: "member"}, want: 503, calls: 1},
		{name: "empty member id", principal: pat.Principal{TokenID: "selector"}, want: 503, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := 0
			h, err := NewOathkeeper(testSecret, oathkeeperTestHeader, authenticateFunc(func(ctx context.Context, token string) (pat.Principal, error) {
				called++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > authenticationTimeout {
					t.Error("missing bounded deadline")
				}
				if token != oathkeeperTestToken {
					t.Error("unexpected token")
				}
				return tc.principal, tc.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			r := oathkeeperRequest()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != tc.calls {
				t.Fatalf("status %d calls %d, want %d/%d", w.Code, called, tc.want, tc.calls)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
				t.Error("missing no-store JSON headers")
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if tc.want == 200 {
				if response["subject"] != "member" || len(response) != 2 {
					t.Fatalf("unexpected success response: %v", response)
				}
				extra, ok := response["extra"].(map[string]any)
				if !ok || len(extra) != 2 || extra["member_id"] != "member" || extra["token_id"] != "selector" {
					t.Fatalf("unexpected extra: %v", response["extra"])
				}
			} else if len(response) != 1 || response["error"] != http.StatusText(tc.want) {
				t.Fatalf("unexpected error response: %v", response)
			}
			if tc.want == 401 && w.Header().Get("WWW-Authenticate") != `Bearer realm="Oathkeeper PAT"` {
				t.Error("missing bearer challenge")
			}
			if tc.want == 405 && w.Header().Get("Allow") != http.MethodGet {
				t.Error("missing allowed method")
			}
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), testSecret) {
				t.Error("private value leaked")
			}
		})
	}
}

func TestOathkeeperNeverCachesAuthentication(t *testing.T) {
	calls := 0
	h, err := NewOathkeeper(testSecret, oathkeeperTestHeader, authenticateFunc(func(context.Context, string) (pat.Principal, error) {
		calls++
		if calls%2 == 0 {
			return pat.Principal{}, pat.ErrInvalidToken
		}
		return pat.Principal{MemberID: "member", TokenID: "selector"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{200, 401, 200, 401} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, oathkeeperRequest())
		if w.Code != want {
			t.Fatalf("status %d want %d", w.Code, want)
		}
	}
	if calls != 4 {
		t.Fatalf("authentication calls %d want 4", calls)
	}
}

func TestOathkeeperRecordsRedactedFailureCause(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h, err := NewOathkeeper(testSecret, oathkeeperTestHeader, authenticateFunc(func(context.Context, string) (pat.Principal, error) {
		return pat.Principal{}, fmt.Errorf("database unavailable: %s %s", oathkeeperTestToken, testSecret)
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, recorder := failurecause.WithRecorder(t.Context())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, oathkeeperRequest().WithContext(ctx))
	failure, recorded := recorder.Recorded()
	if w.Code != 503 || logs.Len() != 0 || !recorded || !strings.HasPrefix(failure.Cause, "database unavailable:") ||
		strings.Contains(failure.Cause, oathkeeperTestToken) || strings.Contains(failure.Cause, testSecret) {
		t.Fatalf("failure cause missing, logged twice or private values leaked: %+v %s", failure, logs.String())
	}
}

func TestOathkeeperSaturationAndCancellation(t *testing.T) {
	entered := make(chan struct{}, maximumConcurrentChecks)
	h, err := NewOathkeeper(testSecret, oathkeeperTestHeader, authenticateFunc(func(ctx context.Context, _ string) (pat.Principal, error) {
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
			h.ServeHTTP(w, oathkeeperRequest().WithContext(ctx))
			if w.Code != 503 {
				t.Errorf("cancel status %d", w.Code)
			}
		})
	}
	for range maximumConcurrentChecks {
		<-entered
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, oathkeeperRequest())
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturation status %d", w.Code)
	}
	cancel()
	wg.Wait()
	if len(h.slots) != 0 {
		t.Error("slot leak")
	}
}

func TestOathkeeperConstructorRejectsMissingDependencies(t *testing.T) {
	for _, tc := range []struct{ secret, header string }{
		{"", oathkeeperTestHeader}, {" ", oathkeeperTestHeader}, {testSecret, ""},
		{testSecret, "X Invalid"}, {testSecret, "authorization"},
	} {
		if _, err := NewOathkeeper(tc.secret, tc.header, authenticateFunc(nil)); err == nil {
			t.Error("accepted invalid configuration")
		}
	}
	if _, err := NewOathkeeper(testSecret, oathkeeperTestHeader, nil); err == nil {
		t.Error("accepted missing authenticator")
	}
}
