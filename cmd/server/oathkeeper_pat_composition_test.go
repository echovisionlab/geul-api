package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

type oathkeeperPATPrincipal struct{}

func (oathkeeperPATPrincipal) Authenticate(context.Context, string) (pat.Principal, error) {
	return pat.Principal{MemberID: "member", TokenID: "selector"}, nil
}

func TestOathkeeperPATOnlyOnPrivateListenerWithInternalCredential(t *testing.T) {
	const secret = "internal-service-secret"
	handler, err := patauth.NewOathkeeper(secret, testInternalServiceHeaderName, oathkeeperPATPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	privateServer, err := newMCPPrivateHTTPServer(mcpPrivateHandlers{
		authorAdmission: http.NotFoundHandler(),
		oathkeeperPAT:   handler,
	}, &config.Config{MCPPrivatePort: 8001})
	if err != nil {
		t.Fatal(err)
	}
	if privateServer.Addr != ":8001" {
		t.Fatalf("private address %q", privateServer.Addr)
	}
	// The main mux retains the opt-in legacy verifier, but the new handler
	// is supplied only to the private server in the production composition.
	mainMux := http.NewServeMux()
	if err := registerPATVerification(mainMux, "legacy-worker-verification-secret", deniedPAT{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		handler    http.Handler
		method     string
		path       string
		credential string
		want       int
	}{
		{"private", privateServer.Handler, http.MethodGet, patauth.OathkeeperPath, secret, 200},
		{"missing caller", privateServer.Handler, http.MethodGet, patauth.OathkeeperPath, "", 401},
		{"wrong caller", privateServer.Handler, http.MethodGet, patauth.OathkeeperPath, "worker-secret", 401},
		{"method", privateServer.Handler, http.MethodPost, patauth.OathkeeperPath, secret, 405},
		{"suffix", privateServer.Handler, http.MethodGet, patauth.OathkeeperPath + "/extra", secret, 404},
		{"public", mainMux, http.MethodGet, patauth.OathkeeperPath, secret, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer pat_selector.secret")
			r.Header.Set(testInternalServiceHeaderName, tc.credential)
			w := httptest.NewRecorder()
			tc.handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d want %d", w.Code, tc.want)
			}
		})
	}
}
