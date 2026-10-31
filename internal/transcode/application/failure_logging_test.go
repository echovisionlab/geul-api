package application

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

// The queue consumer logs a failed result once; the handler only names the
// event's object on the error it returns.
func TestResultHandlersReturnCauseWithEventIdentifiers(t *testing.T) {
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

			err := handle(t.Context(), []byte{0xff})

			require.Error(t, err)
			require.Empty(t, output.String())
			failure := failurecause.Of(err)
			require.Contains(t, failure.Cause, "unmarshal")
			keys := make([]string, 0, len(failure.Attrs))
			for _, attr := range failure.Attrs {
				keys = append(keys, attr.Key)
			}
			require.ElementsMatch(t, []string{"event_id", "file_id", "entity_type", "entity_id"}, keys)
		})
	}
}
