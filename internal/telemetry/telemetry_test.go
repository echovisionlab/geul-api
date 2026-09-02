package telemetry

import (
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

func TestCanonicalResourceServiceNameCannotBeOverriddenByEnvironment(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "wrong-service")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=also-wrong,deployment.environment=test")

	res, err := newCanonicalResource(t.Context(), sharedtelemetry.ServiceBackend)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := res.Set().Value(semconv.ServiceNameKey)
	if !ok || value.AsString() != sharedtelemetry.ServiceBackend.String() {
		t.Fatalf("service.name = %q, want %q", value.AsString(), sharedtelemetry.ServiceBackend)
	}
}

func TestInitRejectsUnknownServiceNameBeforeCreatingExporters(t *testing.T) {
	if result, err := Init(t.Context(), sharedtelemetry.ServiceName("wrong-service")); result != nil || err == nil {
		t.Fatalf("Init() result = %#v, error = %v", result, err)
	}
}
