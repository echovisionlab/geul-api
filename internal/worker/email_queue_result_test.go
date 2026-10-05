package worker

import (
	"errors"
	"testing"

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
			outcome, err := emailDeliveryQueueResult(tt.result, tt.deliveryErr)
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
