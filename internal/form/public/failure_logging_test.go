package public

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/crypto"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestInvalidPasswordHashLogsCauseAndStillReturnsFalse(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	service := &Service{password: &crypto.PasswordHasher{}}
	require.False(t, service.verifyFormPassword(t.Context(), "private-password", "broken-private-hash"))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "verify_form_password", entry["operation"])
	require.NotEmpty(t, entry["failure_cause"])
	require.NotContains(t, output.String(), "private-password")
	require.NotContains(t, output.String(), "broken-private-hash")
}

// Malformed submission JSON is client input: request.completed records the
// InvalidArgument once, and validation itself logs nothing.
func TestMalformedSubmissionJSONIsRejectedWithoutLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	err := validateFormSubmissionDataPayload([]byte(`{"private":"private-name",`))
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Empty(t, output.String())
}
