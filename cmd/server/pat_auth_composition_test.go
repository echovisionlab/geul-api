package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	"github.com/echovisionlab/geul-api/internal/member/pat"
)

type deniedPAT struct{}

func (deniedPAT) Authenticate(context.Context, string) (pat.Principal, error) {
	return pat.Principal{}, pat.ErrInvalidToken
}

func TestPATVerificationRouteOptIn(t *testing.T) {
	for _, tc := range []struct {
		secret     string
		wantError  bool
		wantStatus int
	}{
		{"", false, 404}, {"short", true, 404}, {"pat-verification-gateway-secret-for-tests", false, 401},
	} {
		mux := http.NewServeMux()
		err := registerPATVerification(mux, tc.secret, deniedPAT{})
		if (err != nil) != tc.wantError {
			t.Fatalf("registration error %v", err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, patauth.Path, nil))
		if w.Code != tc.wantStatus {
			t.Errorf("status %d want %d", w.Code, tc.wantStatus)
		}
	}
}
