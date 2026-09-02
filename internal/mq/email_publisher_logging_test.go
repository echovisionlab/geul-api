package mq

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

func TestEmailQueuePublishLogUsesStablePIISafeFields(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	commandID := "email-command-123"
	emitQueuePublishResult(context.Background(), "email.send", commandID, 4*time.Millisecond, "")

	var entry structured.Fields
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(output.String())), &entry))
	require.Equal(t, "queue", entry["domain"])
	require.Equal(t, "queue.publish.succeeded", entry["event"])
	require.Equal(t, "succeeded", entry["outcome"])
	require.NotContains(t, entry, "exchange")
	require.NotContains(t, entry, "routing_key")
	require.Equal(t, "email.send", entry["queue"])
	require.Equal(t, commandID, entry["command_id"])
	require.Equal(t, commandID, entry["message_id"])
	require.Equal(t, float64(4), entry["duration_ms"])
	require.NotContains(t, entry, "recipient")
}
