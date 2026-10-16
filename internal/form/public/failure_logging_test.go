package public

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestInvalidPasswordHashLogsCauseAndStillReturnsFalse(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	service := &FormService{password: &crypto.PasswordHasher{}}
	require.False(t, service.verifyFormPassword("private-password", "broken-private-hash"))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "verify_form_password", entry["operation"])
	require.NotEmpty(t, entry["failure_cause"])
	require.NotContains(t, output.String(), "private-password")
	require.NotContains(t, output.String(), "broken-private-hash")
}
