package mediadelivery

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliveryTokenIsRedactedOnlyForTelemetryLayer(t *testing.T) {
	const originalPath = "/media/payload.signature/11111111-1111-4111-8111-111111111111.glb"
	var telemetryPath, applicationPath string
	application := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		applicationPath = r.URL.Path
	})
	telemetry := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telemetryPath = r.URL.Path
		restoreDeliveryPath(application).ServeHTTP(w, r)
	})

	redactDeliveryTokenForTelemetry(telemetry).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, originalPath, nil),
	)
	if telemetryPath != "/media/[redacted]/11111111-1111-4111-8111-111111111111.glb" {
		t.Fatalf("telemetry path = %q", telemetryPath)
	}
	if applicationPath != originalPath {
		t.Fatalf("application path = %q", applicationPath)
	}
}

func TestNonMediaPathIsUnchangedForTelemetry(t *testing.T) {
	if got := redactedDeliveryPath("/asset/11111111-1111-4111-8111-111111111111/image.webp"); got != "/asset/11111111-1111-4111-8111-111111111111/image.webp" {
		t.Fatalf("redacted asset path = %q", got)
	}
}

func TestTelemetryRedactionPreservesQueryAndHandlesMissingState(t *testing.T) {
	const target = "/media/payload.signature/11111111-1111-4111-8111-111111111111.glb?w=320"
	var telemetryURI, appURI string
	application := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { appURI = r.RequestURI })
	telemetryHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telemetryURI = r.RequestURI
		restoreDeliveryPath(application).ServeHTTP(w, r)
	})
	redactDeliveryTokenForTelemetry(telemetryHandler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	if telemetryURI != "/media/[redacted]/11111111-1111-4111-8111-111111111111.glb?w=320" || appURI != target {
		t.Fatalf("telemetryURI=%q appURI=%q", telemetryURI, appURI)
	}

	called := false
	restoreDeliveryPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if !called {
		t.Fatal("restore middleware did not pass through request without saved state")
	}
	for _, path := range []string{"/media/token-only", "/media/", "/asset/id/image.webp"} {
		if got := redactedDeliveryPath(path); got != path {
			t.Fatalf("redactedDeliveryPath(%q) = %q", path, got)
		}
	}

	unchangedPath := ""
	redactDeliveryTokenForTelemetry(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { unchangedPath = r.URL.Path })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if unchangedPath != "/health" {
		t.Fatalf("unchanged path = %q", unchangedPath)
	}
}
