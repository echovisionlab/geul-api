package aieditor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func captureEditorFailures(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestConsumedEditorFailuresLogCauseAndTurn(t *testing.T) {
	for _, stage := range []string{"open", "read", "start", "generate", "parse"} {
		t.Run(stage, func(t *testing.T) {
			output := captureEditorFailures(t)
			document := newFakeDocument()
			provider := &fakeProvider{session: &fakeSession{responses: []string{"{"}}}
			cause := errors.New("upstream connection refused token=private-token")
			switch stage {
			case "open":
				document.openErr = cause
			case "read":
				document.readErr = cause
			case "start":
				provider.err = cause
			case "generate":
				provider.session.errors = []error{cause}
			}
			service, err := NewService(document, provider)
			require.NoError(t, err)
			var terminal *managev1.AIEditorTurnTerminalOutcome
			require.NoError(t, service.start(t.Context(), testOwner, newStartRequest(), func(event *managev1.AIEditorTurnEvent) error {
				if event.GetTerminal() != nil {
					terminal = event.GetTerminal()
				}
				return nil
			}))
			require.NotNil(t, terminal)
			require.Equal(t, managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_FAILED, terminal.Status)
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.NotEmpty(t, record["turn_id"])
			require.Equal(t, "improve-writing", record["action"])
			if stage != "parse" {
				require.Contains(t, record["failure_cause"], "connection refused")
			} else {
				require.Contains(t, record["failure_cause"], "unexpected EOF")
			}
			require.NotContains(t, output.String(), "private-token")
		})
	}
}

type closeFailureSession struct{ llm.Session }

func (closeFailureSession) Close(context.Context) error {
	return errors.New("release session: connection reset")
}

func TestEditorSessionCloseFailureIsLogged(t *testing.T) {
	output := captureEditorFailures(t)
	closeProviderSession(closeFailureSession{}, "turn-1")
	require.Contains(t, output.String(), "connection reset")
	require.Contains(t, output.String(), `"turn_id":"turn-1"`)
}
