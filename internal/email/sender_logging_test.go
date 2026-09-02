package email

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

func TestAdapterLifecycleLogUsesCorrelatablePIISafeFields(t *testing.T) {
	logOutput := captureEmailPackageLogs(t)
	message := &Email{
		CommandID: "command-123",
		MessageID: "<logical-123@localhost>",
		Template:  "login_code",
		To:        "private-recipient@example.test",
		Subject:   "private subject",
		Text:      "private body",
	}

	logAdapterLifecycle(
		t.Context(),
		slog.LevelInfo,
		"Email accepted by provider",
		"mail.provider.accepted",
		"adapter-1",
		"SES primary",
		message,
		"provider-123",
		"accepted",
		"",
		"",
	)

	entry := decodeEmailPackageLog(t, logOutput)
	require.Equal(t, "mail", entry["domain"])
	require.Equal(t, "mail.provider.accepted", entry["event"])
	require.Equal(t, "command-123", entry["command_id"])
	require.Equal(t, "<logical-123@localhost>", entry["logical_message_id"])
	require.Equal(t, "provider-123", entry["provider_message_id"])
	require.Equal(t, "login_code", entry["template_type"])
	require.Equal(t, "adapter-1", entry["adapter_id"])
	require.Equal(t, "SES primary", entry["adapter_name"])
	require.Equal(t, "accepted", entry["outcome"])
	require.NotContains(t, entry, "message_id")
	require.NotContains(t, entry, "recipient")
	require.NotContains(t, entry, "to")
	require.NotContains(t, logOutput.String(), "private-recipient@example.test")
	require.NotContains(t, logOutput.String(), "private subject")
	require.NotContains(t, logOutput.String(), "private body")
}

func captureEmailPackageLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})
	return &output
}

func decodeEmailPackageLog(t *testing.T, output *bytes.Buffer) structured.Fields {
	t.Helper()
	line := strings.TrimSpace(output.String())
	require.NotEmpty(t, line)
	var entry structured.Fields
	require.NoError(t, json.Unmarshal([]byte(line), &entry))
	return entry
}
