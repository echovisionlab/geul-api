package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/adapters/opendiscogsauth"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

type deniedOpenDiscogsToken struct{}

func (deniedOpenDiscogsToken) Authenticate(context.Context, string) (pat.Principal, error) {
	return pat.Principal{}, pat.ErrInvalidToken
}

func TestOpenDiscogsRouteOptIn(t *testing.T) {
	for _, tc := range []struct {
		secret     string
		wantError  bool
		wantStatus int
	}{
		{"", false, 404}, {"short", true, 404}, {"opendiscogs-gateway-secret-for-tests", false, 401},
	} {
		mux := http.NewServeMux()
		err := registerOpenDiscogsAuthentication(mux, tc.secret, deniedOpenDiscogsToken{})
		if (err != nil) != tc.wantError {
			t.Fatalf("registration error %v", err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, opendiscogsauth.Path, nil))
		if w.Code != tc.wantStatus {
			t.Errorf("status %d want %d", w.Code, tc.wantStatus)
		}
	}
}
