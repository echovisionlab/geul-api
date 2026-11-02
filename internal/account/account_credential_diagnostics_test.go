package account

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/stretchr/testify/require"
)

func TestCredentialHookFailureCarriesRedactedDiagnosis(t *testing.T) {
	// The hook endpoint's failure record renders the error with
	// failurecause.Of; the attributes keep it actionable after redaction.
	for _, tt := range []struct {
		name, code string
		err        error
		complete   bool
	}{
		{"missing snapshot", "credential_snapshot_missing", fmt.Errorf("wrapped: %w", ErrAccountCredentialSnapshotMissing), false},
		{"invalid mutation", "credential_mutation_invalid", ErrAccountCredentialMutationShape, false},
		{"recovery policy", "recoverable_auth_method", ErrAccountCredentialUnrecoverable, false},
		{"email policy", "canonical_email_provider_required", ErrMemberPrimaryEmailUnavailable, false},
		{"committed mismatch", "credential_committed_mismatch", ErrAccountCredentialCommittedMismatch, true},
		{"upstream failure", "credential_hook_failed", errors.New("upstream unavailable token=secret-token person@example.test"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stage := "validate"
			if tt.complete {
				stage = "complete"
			}
			err := annotateCredentialHookFailure(stage, AccountCredentialHookInput{Kind: AccountCredentialPasskey}, tt.err)
			require.ErrorIs(t, err, tt.err)
			failure := failurecause.Of(err)
			attrs := map[string]slog.Value{}
			for _, attr := range failure.Attrs {
				attrs[attr.Key] = attr.Value
			}
			require.Equal(t, tt.code, attrs["error_code"].String())
			require.Equal(t, stage, attrs["stage"].String())
			require.Equal(t, "passkey", attrs["credential_type"].String())
			require.False(t, attrs["proposed_snapshot_present"].Bool())
			require.False(t, attrs["previous_snapshot_present"].Bool())
			for _, private := range []string{"secret-token", "person@example.test"} {
				require.NotContains(t, failure.Cause, private)
			}
		})
	}
}
