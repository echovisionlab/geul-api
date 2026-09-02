package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestEmailLifecycleLogUsesCorrelatablePIISafeFields(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	commandID := "command-123"
	job := &managev1.SendEmailEvent{
		MessageId:    &commandID,
		Recipient:    "private-recipient@example.test",
		TemplateType: "login_code",
	}
	logEmailLifecycle(
		t.Context(),
		slog.LevelError,
		"Email adapter attempt failed",
		"mail.adapter.failed",
		job,
		"<logical-123@localhost>",
		"failed",
		"adapter_send_failed",
		"rate_limited",
		"adapter_id", "adapter-1",
		"adapter_name", "SES primary",
		"provider_message_id", "provider-123",
		"recipient", job.Recipient,
		"to", job.Recipient,
		"error", errors.New("provider rejected private-recipient@example.test"),
	)

	line := strings.TrimSpace(output.String())
	require.NotEmpty(t, line)
	var entry structured.Fields
	require.NoError(t, json.Unmarshal([]byte(line), &entry))
	require.Equal(t, "mail", entry["domain"])
	require.Equal(t, "mail.adapter.failed", entry["event"])
	require.Equal(t, "command-123", entry["command_id"])
	require.Equal(t, "<logical-123@localhost>", entry["logical_message_id"])
	require.Equal(t, "provider-123", entry["provider_message_id"])
	require.Equal(t, "login_code", entry["template_type"])
	require.Equal(t, "adapter-1", entry["adapter_id"])
	require.Equal(t, "SES primary", entry["adapter_name"])
	require.Equal(t, "failed", entry["outcome"])
	require.Equal(t, "adapter_send_failed", entry["reason"])
	require.Equal(t, "rate_limited", entry["error_type"])
	require.NotContains(t, entry, "recipient")
	require.NotContains(t, entry, "to")
	require.NotContains(t, entry, "error")
	require.NotContains(t, line, "private-recipient@example.test")
}

func TestSafeMailLogAttrsDropsSensitivePayloadFields(t *testing.T) {
	safe := safeMailLogAttrs(structured.Values{
		"recipient", "private@example.test",
		"subject", "secret subject",
		"body", "secret body",
		"token", "secret-token",
		"error", errors.New("private@example.test"),
		"command_id", "command-123",
	})

	require.Equal(t, structured.Values{"command_id", "command-123"}, safe)
}

func TestDurableEmailMessageIDHasNoRandomFallback(t *testing.T) {
	require.Empty(t, durableEmailMessageID("", "", ""))
	require.Equal(t, "<command-123@localhost>", durableEmailMessageID("", "command-123"))
}

func TestAcceptedProviderMessageIDRejectsMissingAdapterID(t *testing.T) {
	for _, result := range []*email.SendResult{nil, {}, {MessageID: " \t "}} {
		messageID, err := acceptedProviderMessageID(result)
		require.Empty(t, messageID)
		require.Error(t, err)
	}

	messageID, err := acceptedProviderMessageID(&email.SendResult{MessageID: " provider-123 "})
	require.NoError(t, err)
	require.Equal(t, "provider-123", messageID)
}

func TestRetryableEmailDeliveryErrorDoesNotExposeProviderResponse(t *testing.T) {
	err := retryableEmailDeliveryError("rate_limited")
	require.EqualError(t, err, "mail delivery retry requested: rate_limited")
	require.NotContains(t, err.Error(), "@")
}
