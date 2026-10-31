package failurecause

import (
	"errors"
	"fmt"
	"log/slog"
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
	if _, ok := RecorderFrom(t.Context()).Recorded(); ok {
		t.Fatal("empty context reported a recorded cause")
	}
	ctx, recorder := WithRecorder(t.Context())
	if RecorderFrom(ctx) != recorder {
		t.Fatal("recorder not found in its context")
	}
	recorder.Record(Failure{Cause: "first"})
	recorder.Record(Failure{Cause: "panic cause", Stack: "goroutine 1 [running]", Attrs: []slog.Attr{slog.String("page_id", "p1")}})
	failure, ok := recorder.Recorded()
	if !ok || failure.Cause != "panic cause" || failure.Stack != "goroutine 1 [running]" || len(failure.Attrs) != 1 {
		t.Fatalf("Recorded() = %+v, %v", failure, ok)
	}
}

type upstreamError struct{ body string }

func (e *upstreamError) Error() string           { return "upstream returned 409: " + e.body }
func (e *upstreamError) PrivateValues() []string { return []string{e.body} }

// Private values and identifiers travel with the error, so every sink redacts
// and annotates the same way however deep the error ends up.
func TestChainCarriesPrivateValuesAndAttrsThroughWrapsAndJoins(t *testing.T) {
	upstream := &upstreamError{body: `{"traits":{"name":"Private Person"}}`}
	nickname := WithPrivate(errors.New("nickname quiet-river is taken"), "quiet-river")
	err := fmt.Errorf("complete onboarding: %w", WithAttrs(
		errors.Join(
			fmt.Errorf("lock identity: %w", upstream),
			WithAttrs(fmt.Errorf("reserve: %w", nickname), slog.String("member_id_hint", "inner"), slog.String("locale", "ko")),
		),
		slog.String("member_id_hint", "outer"),
	))

	got := Message(err)
	for _, private := range []string{"Private Person", "quiet-river"} {
		if strings.Contains(got, private) {
			t.Fatalf("Message() kept %q: %q", private, got)
		}
	}
	if want := "complete onboarding: lock identity: upstream returned 409: [redacted]\nreserve: nickname [redacted] is taken"; got != want {
		t.Fatalf("Message() = %q, want %q", got, want)
	}
	if err.Error() == got {
		t.Fatal("annotation must not change the error text")
	}
	if !errors.Is(err, upstream) {
		t.Fatal("annotation broke errors.Is")
	}
	attrs := Attrs(err)
	if len(attrs) != 2 || attrs[0].String() != "member_id_hint=outer" || attrs[1].String() != "locale=ko" {
		t.Fatalf("Attrs() = %v", attrs)
	}
	failure := Of(err, "complete")
	if !strings.HasPrefix(failure.Cause, "[redacted] onboarding:") || len(failure.Attrs) != 2 {
		t.Fatalf("Of() = %+v", failure)
	}
	if WithPrivate(nil, "x") != nil || WithAttrs(nil, slog.String("k", "v")) != nil {
		t.Fatal("annotating nil must stay nil")
	}
}

func TestServerFaultSurvivesWrapsAndJoins(t *testing.T) {
	fault := ServerFault(errors.New("decode stored snapshot: unexpected EOF"))
	err := fmt.Errorf("restore version: %w", errors.Join(errors.New("other"), WithAttrs(fault, slog.String("page_id", "p1"))))
	if !IsServerFault(err) || !Of(err).ServerFault {
		t.Fatal("server fault lost through wrapping")
	}
	if IsServerFault(errors.New("not found")) || ServerFault(nil) != nil {
		t.Fatal("unmarked errors are not server faults")
	}
	if err.Error() != "restore version: other\ndecode stored snapshot: unexpected EOF" {
		t.Fatalf("marking changed the text: %q", err.Error())
	}
}

func TestPanicRedactsPrivateValuesOfAnErrorValue(t *testing.T) {
	cause, _ := Panic(fmt.Errorf("lock identity: %w", &upstreamError{body: "traits quiet-river"}))
	if cause != "lock identity: upstream returned 409: [redacted]" {
		t.Fatalf("Panic cause = %q", cause)
	}
}
