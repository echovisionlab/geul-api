package mq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestConsumerLogsOriginalCauseBeforeSettlement(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			connection, mock := mockConnection(t)
			cause := fmt.Errorf("process media: %w", errors.New("disk full private@example.test token=hidden"))
			consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
				if terminal {
					return terminalTestError{cause}
				}
				return retryableTestError{cause}
			})
			require.NoError(t, err)
			if terminal {
				expectBoolean(mock, "delete", true, nil)
			} else {
				expectRetry(mock, 5, nil)
			}
			consumer.process(context.Background(), validMessage(t, 1))
			found := false
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var entry map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &entry))
				if entry["msg"] != "PGMQ handler failed" {
					continue
				}
				found = true
				require.Contains(t, entry["failure_cause"], "process media: disk full")
				require.Equal(t, "command-1", entry["message_id"])
				require.Equal(t, "command-1", entry["command_id"])
				require.Equal(t, consumer.config.Name, entry["queue"])
			}
			require.True(t, found)
			require.NotContains(t, output.String(), "private@example.test")
			require.NotContains(t, output.String(), "hidden")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
