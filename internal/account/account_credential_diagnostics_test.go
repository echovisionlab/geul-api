package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestCredentialHookLogsSurviveProductionRedaction(t *testing.T) {
	// Uses the production handler, redactor, and context propagation together.
	// A raw error or a named string enum alone is deliberately removed by the
	// redactor and cannot provide actionable diagnostics.
	for _, tt := range []struct {
		name, code, level string
		err               error
		complete          bool
	}{
		{"missing snapshot", "credential_snapshot_missing", "ERROR", fmt.Errorf("wrapped: %w", ErrAccountCredentialSnapshotMissing), false},
		{"invalid mutation", "credential_mutation_invalid", "ERROR", ErrAccountCredentialMutationShape, false},
		{"recovery policy", "recoverable_auth_method", "WARN", ErrAccountCredentialUnrecoverable, false},
		{"email policy", "canonical_email_provider_required", "WARN", ErrMemberPrimaryEmailUnavailable, false},
		{"committed mismatch", "credential_committed_mismatch", "ERROR", ErrAccountCredentialCommittedMismatch, true},
		{"upstream failure", "credential_hook_failed", "ERROR", errors.New("secret-token person@example.test"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			// This is what the stock Kratos v26.2.0 template actually produces:
			// Identity.MarshalJSON strips credentials from both identities.
			body := `{"identity_id":"private-identity","flow_id":"private-flow","credentials":{},"credentials_present":false,"previous_credentials":{},"previous_credentials_present":false}`
			r := httptest.NewRequest(http.MethodPost, "/hooks/pre-settings-passkey", bytes.NewBufferString(body))
			requestContext, err := telemetry.NewPropagatedRequestContext("11111111-1111-4111-8111-111111111111", telemetry.SystemActor{ServiceName: "geul-identity"})
			require.NoError(t, err)
			ctx := telemetry.WithRequestContext(r.Context(), requestContext)
			span := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
			r = r.WithContext(trace.ContextWithSpanContext(ctx, span))
			stage := "validate"
			if tt.complete {
				stage = "complete"
			}
			logCredentialHookFailure(r.Context(), stage, AccountCredentialHookInput{Kind: AccountCredentialPasskey}, tt.err)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tt.code, entry["error_code"])
			require.Equal(t, tt.level, entry["level"])
			require.Equal(t, stage, entry["stage"])
			require.Equal(t, "passkey", entry["credential_type"])
			require.Equal(t, false, entry["proposed_snapshot_present"])
			require.Equal(t, false, entry["previous_snapshot_present"])
			require.Equal(t, "11111111-1111-4111-8111-111111111111", entry["request_id"])
			require.Equal(t, span.TraceID().String(), entry["trace_id"])
			require.Equal(t, span.SpanID().String(), entry["span_id"])
			for _, private := range []string{"private-identity", "private-flow", "secret-token", "person@example.test"} {
				require.NotContains(t, output.String(), private)
			}
		})
	}
}
