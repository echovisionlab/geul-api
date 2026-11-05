package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestEmailDeliveryQueueResult(t *testing.T) {
	tests := []struct {
		name              string
		result            emaildelivery.DeliveryResult
		deliveryErr       error
		wantOutcome       emaildelivery.DeliveryOutcome
		wantError         string
		wantTerminalClass string
	}{
		{
			name:        "application outcome passes through",
			result:      emaildelivery.DeliveryResult{Outcome: emaildelivery.DeliverySuppressed},
			wantOutcome: emaildelivery.DeliverySuppressed,
		},
		{
			name: "retryable provider failure maps to queue retry",
			result: emaildelivery.DeliveryResult{
				Outcome:   emaildelivery.DeliveryProviderFailed,
				Retryable: true,
				ErrorType: "rate_limited",
				Err:       errors.New("provider throttled"),
			},
			wantError: "mail delivery retry requested: rate_limited",
		},
		{
			name: "permanent provider failure maps to terminal queue error",
			result: emaildelivery.DeliveryResult{
				Outcome:   emaildelivery.DeliveryProviderFailed,
				ErrorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(),
				Err:       errors.New("recipient rejected"),
			},
			wantOutcome:       emaildelivery.DeliveryProviderFailed,
			wantError:         "recipient rejected",
			wantTerminalClass: "email_invalid_recipient",
		},
		{
			name: "accepted send with failed terminal write remains a retryable queue error",
			result: emaildelivery.DeliveryResult{
				Outcome: emaildelivery.DeliveryAccepted,
			},
			deliveryErr: errors.New("database unavailable"),
			wantError:   "database unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := emailDeliveryQueueResult(t.Context(), nil, tt.result, tt.deliveryErr)
			require.Equal(t, tt.wantOutcome, outcome)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantError)
			terminalClass, terminal := mq.TerminalDeliveryErrorClass(err)
			require.Equal(t, tt.wantTerminalClass != "", terminal)
			require.Equal(t, tt.wantTerminalClass, terminalClass)
		})
	}
}

func TestEmailQueueRetryLogsOriginalCauseAndRenderContext(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	locale := "ko"
	job := &managev1.SendEmailEvent{TemplateType: "template:welcome", Locale: &locale, Recipient: "private@example.test"}
	result := emaildelivery.DeliveryResult{Retryable: true, ErrorType: "rate_limited", Err: &email.RenderError{LayoutID: "layout-1", Locale: locale, Err: errors.New("provider quota exhausted for private@example.test")}}
	_, err := emailDeliveryQueueResult(t.Context(), job, result, nil)
	require.EqualError(t, err, "mail delivery retry requested: rate_limited")
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "mail.worker.provider_failed", entry["event"])
	require.Contains(t, entry["failure_cause"], "provider quota exhausted")
	require.Equal(t, "template:welcome", entry["template_type"])
	require.Equal(t, "ko", entry["locale"])
	require.Equal(t, "layout-1", entry["layout_id"])
	require.NotContains(t, output.String(), job.Recipient)
}
