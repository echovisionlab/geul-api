package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestResultHandlersLogCauseThroughNormalization(t *testing.T) {
	handlers := &Handlers{}
	for name, handle := range map[string]func(context.Context, []byte) error{
		"HandleTranscodeComplete": handlers.HandleTranscodeComplete,
		"HandleWaveformProgress":  handlers.HandleWaveformProgress,
		"HandleWaveformComplete":  handlers.HandleWaveformComplete,
		"HandleWaveformFail":      handlers.HandleWaveformFail,
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			require.Error(t, handle(t.Context(), []byte{0xff}))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, name, entry["operation"])
			require.Contains(t, entry["failure_cause"], "unmarshal")
			require.Equal(t, "ERROR", entry["level"])
		})
	}
}
