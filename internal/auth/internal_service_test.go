package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testInternalServiceHeaderName = "X-Internal-Service"

func TestRequireInternalServiceSecret(t *testing.T) {
	t.Parallel()

	const secret = "test-internal-service-secret"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, tc := range []struct {
		name       string
		provided   string
		wantStatus int
	}{
		{name: "accepts exact secret", provided: secret, wantStatus: http.StatusNoContent},
		{name: "rejects missing secret", wantStatus: http.StatusUnauthorized},
		{name: "rejects wrong secret", provided: "wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "rejects wrong-length secret", provided: "x", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/internal", nil)
			if tc.provided != "" {
				req.Header.Set(testInternalServiceHeaderName, tc.provided)
			}
			resp := httptest.NewRecorder()

			RequireInternalServiceSecret(secret, testInternalServiceHeaderName, next).ServeHTTP(resp, req)

			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.Code, tc.wantStatus)
			}
		})
	}
}

func TestIsInternalServiceRequest(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/internal", nil)
	if IsInternalServiceRequest("secret", testInternalServiceHeaderName, request) ||
		IsInternalServiceRequest("", testInternalServiceHeaderName, request) ||
		IsInternalServiceRequest("secret", testInternalServiceHeaderName, nil) {
		t.Fatal("untrusted internal request was accepted")
	}

	request.Header.Set(testInternalServiceHeaderName, "secret")
	if !IsInternalServiceRequest("secret", testInternalServiceHeaderName, request) {
		t.Fatal("exact internal service credential was rejected")
	}
	if IsInternalServiceRequest("other-secret", testInternalServiceHeaderName, request) {
		t.Fatal("wrong internal service credential was accepted")
	}
}

func TestInternalServiceRequestUsesConfiguredHeaderName(t *testing.T) {
	t.Parallel()

	const customHeaderName = "X-Geul-Internal-Service"
	request := httptest.NewRequest(http.MethodPost, "/internal", nil)
	request.Header.Set(customHeaderName, "secret")

	if !IsInternalServiceRequest("secret", customHeaderName, request) {
		t.Fatal("exact credential in configured header was rejected")
	}
	if IsInternalServiceRequest("secret", testInternalServiceHeaderName, request) {
		t.Fatal("credential in a different header was accepted")
	}
}

func TestNormalizeHeaderNameRejectsUnsuitableNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", " X-Auth", "X-Auth ", "X Auth", "X:Auth", "X\tAuth"} {
		if normalized, err := NormalizeHeaderName(name); err == nil || normalized != "" {
			t.Fatalf("NormalizeHeaderName(%q) = %q, %v; want rejection", name, normalized, err)
		}
	}
	for _, name := range []string{"Authorization", "Cookie", "X-Session-Id", "Host", "Content-Length"} {
		if err := ValidateHeaderNames(name, testInternalServiceHeaderName); err == nil {
			t.Fatalf("ValidateHeaderNames(%q, %q) accepted reserved auth name", name, testInternalServiceHeaderName)
		}
	}
}

func TestRequireInternalServiceSecretRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("empty secret", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected empty secret to panic")
			}
		}()
		RequireInternalServiceSecret("", testInternalServiceHeaderName, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	})

	t.Run("invalid header name", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected invalid header name to panic")
			}
		}()
		RequireInternalServiceSecret("secret", "X Invalid", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	})

	t.Run("nil handler", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected nil handler to panic")
			}
		}()
		RequireInternalServiceSecret("secret", testInternalServiceHeaderName, nil)
	})
}
