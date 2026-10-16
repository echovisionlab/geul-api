package emaildelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type failingDeliveryRenderer struct{ err error }

func (r failingDeliveryRenderer) Render(context.Context, *managev1.SendEmailEvent) (*email.RenderedEmail, error) {
	return nil, r.err
}

func TestDeliveryLogsRenderCauseAndContextWithoutPrivateData(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := fmt.Errorf("render email layout: %w", errors.New("Email Layout element <img> is missing its durable unit marker"))
	app := NewDeliveryApplication(&deliveryCampaignStub{needs: true}, recipientAuthorizerStub{}, failingDeliveryRenderer{&email.RenderError{LayoutID: "layout-old", Locale: "ko", Err: cause}}, deliverySuppressionStub{}, providerLoaderStub{}, deliveryMetricsStub{})
	commandID := "command-1"
	result, err := app.Deliver(t.Context(), &managev1.SendEmailEvent{MessageId: &commandID, TemplateType: "template:test", Recipient: "private@example.test", TemplateData: map[string]string{"code": "654321"}})
	require.NoError(t, err)
	require.Equal(t, DeliveryRenderFailed, result.Outcome)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, cause.Error(), entry["failure_cause"])
	require.Equal(t, "template:test", entry["template_type"])
	require.Equal(t, "ko", entry["locale"])
	require.Equal(t, "layout-old", entry["layout_id"])
	require.NotContains(t, output.String(), "private@example.test")
	require.NotContains(t, output.String(), "654321")
}

func TestProviderFailureLogsCauseBeforeWorkerRetryMapping(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	failure := email.NewDeliveryError(email.DeliveryErrorRateLimited, true, fmt.Errorf("provider throttled: %w", errors.New("quota exhausted for reader@example.test token=provider-secret https://provider.test/?credential=hidden")))
	app := NewDeliveryApplication(&deliveryCampaignStub{needs: true}, recipientAuthorizerStub{}, deliveryRendererStub{&email.RenderedEmail{Subject: "Subject", HTML: "<p>Hello</p>", LayoutID: "layout-1", DisplayedLocale: "ja"}}, deliverySuppressionStub{}, providerLoaderStub{[]email.Adapter{deliveryAdapterStub{sendErr: failure}}}, deliveryMetricsStub{})
	commandID := "command-2"
	result, err := app.Deliver(t.Context(), &managev1.SendEmailEvent{MessageId: &commandID, TemplateType: "template:test", Recipient: "reader@example.test"})
	require.NoError(t, err)
	require.True(t, result.Retryable)
	found := false
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["event"] != "mail.adapter.failed" {
			continue
		}
		found = true
		require.Equal(t, "ERROR", entry["level"])
		require.Contains(t, entry["failure_cause"], "provider throttled: quota exhausted")
		require.Equal(t, "layout-1", entry["layout_id"])
		require.Equal(t, "ja", entry["locale"])
	}
	require.True(t, found)
	for _, secret := range []string{"reader@example.test", "provider-secret", "credential=hidden"} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestFailureCauseRedactsCommandValuesAndCredentials(t *testing.T) {
	job := &managev1.SendEmailEvent{Recipient: "private@example.test", TemplateData: map[string]string{"code": "654321", "name": "Private Name"}}
	message := FailureCause(errors.New("wrapped: invalid private@example.test Private Name code 654321 password=smtp-secret authorization=Bearer private-token postgres://user:pass@host/db"), job)
	require.Contains(t, message, "wrapped: invalid")
	for _, private := range []string{"private@example.test", "Private Name", "654321", "smtp-secret", "private-token", "user:pass"} {
		require.NotContains(t, message, private)
	}
}

func TestFailureCauseKeepsMarkupSQLIdentifiersAndURLPath(t *testing.T) {
	const diagnostic = `render: element <img> is missing {unit}; relation "email_template"; https://provider.test/v1/send`
	require.Equal(t, diagnostic, FailureCause(errors.New(diagnostic), nil))
}

func TestAdapterTestFailureLogsCauseWithoutChangingResponse(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	failure := errors.New("verification failed: mailbox@example.test token=secret-value")
	response := sendAdapterTestEmail(t.Context(), deliveryAdapterStub{sendErr: failure}, "mailbox@example.test")
	require.False(t, response.Msg.Success)
	require.Equal(t, failure.Error(), response.Msg.GetError())
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "verification failed")
	require.NotContains(t, output.String(), "mailbox@example.test")
	require.NotContains(t, output.String(), "secret-value")
}

func TestFailureCauseRedactsQuotedProviderCredentials(t *testing.T) {
	message := FailureCause(errors.New(`provider error: {"password":"quoted secret with spaces", "SecretAccessKey":"aws-secret", "AccessKeyId":"key-id"}; authorization: Basic dXNlcjpwYXNz`), nil)
	for _, private := range []string{"quoted secret with spaces", "aws-secret", "key-id", "dXNlcjpwYXNz"} {
		require.NotContains(t, message, private)
	}
	require.Contains(t, message, "provider error:")
}
