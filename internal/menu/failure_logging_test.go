package menu

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestTopologyDecodeFailureLogsCauseBeforeReturningFalse(t *testing.T) {
	for _, tc := range []struct {
		name, stored, requested, level, operation string
	}{
		{"stored", "[", "[]", "ERROR", "compare_stored_topology"},
		{"requested", "[]", "[", "WARN", "compare_requested_topology"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })

			require.False(t, menuItemTopologyEqual([]byte(tc.stored), []byte(tc.requested)))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tc.level, entry["level"])
			require.Equal(t, tc.operation, entry["operation"])
			require.Equal(t, "unexpected end of JSON input", entry["failure_cause"])
		})
	}
}
