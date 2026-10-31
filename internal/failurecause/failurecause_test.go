package failurecause

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMessageKeepsDiagnosticsAndRemovesSecrets(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"incident text survives", "render email layout: internal: Email Layout element <img> is missing its durable unit marker", "render email layout: internal: Email Layout element <img> is missing its durable unit marker"},
		{"sql identifiers survive", `relation "site_settings" does not exist`, `relation "site_settings" does not exist`},
		{"dsn user info", "dial postgres://geul:s3cret@db:5432/geul failed", "dial postgres://[redacted]@db:5432/geul failed"},
		{"url query", `Get "https://api.example/v1/x?token=abc&y=1": EOF`, `Get "https://api.example/v1/x?[redacted]": EOF`},
		{"standalone bearer", "sent Bearer abc.def", "sent Bearer [redacted]"},
		{"bearer", "upstream said Authorization: Bearer abc.def", "upstream said Authorization: [redacted]"},
		{"assignment", "password=hunter22 user=x", "password=[redacted] user=x"},
		{"email", "invalid recipient person@example.co.kr", "invalid recipient [redacted-email]"},
		{"short private ignored", "line 15 col 2", "line 15 col 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Message(errors.New(tc.in), "15"); got != tc.want {
				t.Fatalf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageRedactsPrivateValuesInWrappedChain(t *testing.T) {
	err := fmt.Errorf("send code: %w", errors.New("code 482913 rejected"))
	if got := Message(err, "482913"); got != "send code: code [redacted] rejected" {
		t.Fatalf("Message() = %q", got)
	}
	if Message(nil) != "" {
		t.Fatal("nil error must be empty")
	}
}

func TestMessageTruncates(t *testing.T) {
	if got := Message(errors.New(strings.Repeat("x", maxMessageLength+10))); !strings.HasSuffix(got, "…[truncated]") {
		t.Fatalf("not truncated: %d", len(got))
	}
}

func TestAttrAndPanic(t *testing.T) {
	if a := Attr(errors.New("boom")); a.Key != "failure_cause" || a.Value.String() != "boom" {
		t.Fatalf("Attr = %v", a)
	}
	cause, stack := Panic("token=abc")
	if cause != "token=[redacted]" || !strings.Contains(stack, "(...)") {
		t.Fatalf("Panic = %q %q", cause, stack[:40])
	}
}

type operationContextError struct {
	message, operation string
	cause              error
}

func (e *operationContextError) Error() string { return e.message }
func (e *operationContextError) Unwrap() error { return e.cause }
func (e *operationContextError) OperationContext() (string, string) {
	return "classified: " + e.message, e.operation
}

type classifiedWrapper struct{ cause *operationContextError }

func (w *classifiedWrapper) Error() string { return "classified: " + w.cause.Error() }
func (w *classifiedWrapper) Unwrap() error { return w.cause }

func TestMessageRendersOperationContextHiddenFromErrorText(t *testing.T) {
	inner := errors.New("context deadline exceeded")
	classified := &classifiedWrapper{cause: &operationContextError{
		message:   "context deadline exceeded",
		operation: "load campaign for person@example.test: context deadline exceeded",
		cause:     fmt.Errorf("load campaign: %w", inner),
	}}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"direct":  {classified, "load campaign for [redacted-email]: context deadline exceeded"},
		"wrapped": {fmt.Errorf("worker: %w", classified), "worker: load campaign for [redacted-email]: context deadline exceeded"},
		"joined":  {errors.Join(errors.New("first"), classified), "first\nload campaign for [redacted-email]: context deadline exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Message(tc.err); got != tc.want {
				t.Fatalf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
	if classified.Error() != "classified: context deadline exceeded" {
		t.Fatalf("Error text changed: %q", classified.Error())
	}
}

func TestRecorderCarriesLastCauseThroughContext(t *testing.T) {
	if _, _, ok := RecorderFrom(t.Context()).Recorded(); ok {
		t.Fatal("empty context reported a recorded cause")
	}
	ctx, recorder := WithRecorder(t.Context())
	if RecorderFrom(ctx) != recorder {
		t.Fatal("recorder not found in its context")
	}
	recorder.Record("first", "")
	recorder.Record("panic cause", "goroutine 1 [running]")
	if cause, stack, ok := recorder.Recorded(); !ok || cause != "panic cause" || stack != "goroutine 1 [running]" {
		t.Fatalf("Recorded() = %q, %q, %v", cause, stack, ok)
	}
}
