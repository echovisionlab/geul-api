package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestEmailDeliveryErrorDecisionMatrix(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		expected  string
		retryable bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{name: "typed connection", err: email.NewDeliveryError(email.DeliveryErrorConnection, true, errors.New("timeout")), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(), retryable: true},
		{name: "typed authentication", err: email.NewDeliveryError(email.DeliveryErrorAuthentication, false, errors.New("auth")), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String()},
		{name: "typed rate limit", err: email.NewDeliveryError(email.DeliveryErrorRateLimited, true, errors.New("throttled")), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String(), retryable: true},
		{name: "typed invalid recipient", err: email.NewDeliveryError(email.DeliveryErrorInvalidRecipient, false, errors.New("rejected")), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String()},
		{name: "typed template", err: email.NewDeliveryError(email.DeliveryErrorTemplate, false, errors.New("render")), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String()},
		{name: "shutdown cancellation", err: email.NewDeliveryError(email.DeliveryErrorConnection, false, context.Canceled), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(), retryable: true},
		{name: "adapter deadline", err: email.NewDeliveryError(email.DeliveryErrorConnection, false, context.DeadlineExceeded), expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(), retryable: true},
		{
			name:     "unknown",
			err:      errors.New("provider returned an unexpected response"),
			expected: managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorType, retryable := emailDeliveryErrorDecision(tt.err)
			require.Equal(t, tt.expected, errorType)
			require.Equal(t, tt.retryable, retryable)
		})
	}
}

func TestTerminalEmailDeliveryErrorClassIsStableAndBounded(t *testing.T) {
	require.Equal(t, "email_invalid_recipient", terminalEmailDeliveryErrorClass(managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String()))
	require.Equal(t, "email_adapter_auth_failed", terminalEmailDeliveryErrorClass(managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String()))
	require.Equal(t, "email_adapter_template_error", terminalEmailDeliveryErrorClass(managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String()))
	require.Equal(t, "email_permanent_provider_failure", terminalEmailDeliveryErrorClass("provider text must not reach queue metadata"))
}

func TestDeliveryStatusForEmailErrorMatrix(t *testing.T) {
	tests := []struct {
		errorType string
		status    string
	}{
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(),
			status:    emailDeliveryRetryable,
		},
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String(),
			status:    emailDeliveryRetryable,
		},
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(),
			status:    campaign.CampaignDeliveryRecipientStatusPermanentFailed,
		},
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String(),
			status:    campaign.CampaignDeliveryRecipientStatusBlocked,
		},
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String(),
			status:    campaign.CampaignDeliveryRecipientStatusBlocked,
		},
		{
			errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String(),
			status:    emailDeliveryRetryable,
		},
		{
			errorType: "new-provider-error",
			status:    emailDeliveryRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.errorType, func(t *testing.T) {
			require.Equal(t, tt.status, deliveryStatusForEmailError(tt.errorType))
		})
	}
}

func TestClassifyEmailDeliveryFailurePrioritizesDurableFailureTypes(t *testing.T) {
	invalidRecipientErr := email.NewDeliveryError(email.DeliveryErrorInvalidRecipient, false, errors.New("recipient rejected"))
	authErr := email.NewDeliveryError(email.DeliveryErrorAuthentication, false, errors.New("authentication failed"))
	templateErr := email.NewDeliveryError(email.DeliveryErrorTemplate, false, errors.New("template render failed"))
	timeoutErr := email.NewDeliveryError(email.DeliveryErrorConnection, true, errors.New("smtp timeout"))

	tests := []struct {
		name          string
		failures      []emailAdapterFailure
		fallback      error
		expectedState string
		expectedType  string
		expectedErr   error
	}{
		{
			name: "invalid recipient wins over auth and transient failures",
			failures: []emailAdapterFailure{
				{err: timeoutErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String()},
				{err: authErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String()},
				{err: invalidRecipientErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String()},
			},
			fallback:      timeoutErr,
			expectedState: campaign.CampaignDeliveryRecipientStatusPermanentFailed,
			expectedType:  managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(),
			expectedErr:   invalidRecipientErr,
		},
		{
			name: "auth failure wins over template and transient failures",
			failures: []emailAdapterFailure{
				{err: timeoutErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String()},
				{err: templateErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String()},
				{err: authErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String()},
			},
			fallback:      timeoutErr,
			expectedState: campaign.CampaignDeliveryRecipientStatusBlocked,
			expectedType:  managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String(),
			expectedErr:   authErr,
		},
		{
			name: "template failure wins over transient failures",
			failures: []emailAdapterFailure{
				{err: timeoutErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String()},
				{err: templateErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String()},
			},
			fallback:      timeoutErr,
			expectedState: campaign.CampaignDeliveryRecipientStatusBlocked,
			expectedType:  managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String(),
			expectedErr:   templateErr,
		},
		{
			name:          "falls back to categorized fallback error",
			failures:      []emailAdapterFailure{{err: timeoutErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String()}},
			fallback:      email.NewDeliveryError(email.DeliveryErrorRateLimited, true, errors.New("rate limited")),
			expectedState: emailDeliveryRetryable,
			expectedType:  managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String(),
			expectedErr:   errors.New("rate limited"),
		},
		{
			name:          "nil fallback becomes retryable unknown send failure",
			failures:      nil,
			fallback:      nil,
			expectedState: campaign.CampaignDeliveryRecipientStatusBlocked,
			expectedType:  managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String(),
			expectedErr:   errors.New("email send failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errorType, selectedErr := classifyEmailDeliveryFailure(tt.failures, tt.fallback)

			require.Equal(t, tt.expectedState, status)
			require.Equal(t, tt.expectedType, errorType)
			require.EqualError(t, selectedErr, tt.expectedErr.Error())
		})
	}
}

func TestSummarizeEmailAdapterOutcomeMapsDeliveryDecisions(t *testing.T) {
	invalidRecipientErr := email.NewDeliveryError(email.DeliveryErrorInvalidRecipient, false, errors.New("recipient rejected"))
	rateLimitedErr := email.NewDeliveryError(email.DeliveryErrorRateLimited, true, errors.New("rate limited"))

	tests := []struct {
		name              string
		providerMessageID string
		failures          []emailAdapterFailure
		fallback          error
		wantAccepted      bool
		wantProviderID    string
		wantStatus        string
		wantErrorType     string
		wantError         string
	}{
		{
			name:              "provider acceptance completes delivery",
			providerMessageID: "provider-message-1",
			wantAccepted:      true,
			wantProviderID:    "provider-message-1",
		},
		{
			name:              "failover acceptance overrides prior transient failure",
			providerMessageID: "provider-message-2",
			failures: []emailAdapterFailure{
				{err: rateLimitedErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String()},
			},
			fallback:       rateLimitedErr,
			wantAccepted:   true,
			wantProviderID: "provider-message-2",
		},
		{
			name: "invalid recipient all-failure is permanent",
			failures: []emailAdapterFailure{
				{err: invalidRecipientErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String()},
			},
			fallback:      invalidRecipientErr,
			wantStatus:    campaign.CampaignDeliveryRecipientStatusPermanentFailed,
			wantErrorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(),
			wantError:     invalidRecipientErr.Error(),
		},
		{
			name: "rate limit all-failure remains retryable",
			failures: []emailAdapterFailure{
				{err: rateLimitedErr, errorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String()},
			},
			fallback:      rateLimitedErr,
			wantStatus:    emailDeliveryRetryable,
			wantErrorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String(),
			wantError:     rateLimitedErr.Error(),
		},
		{
			name: "no adapter send result stays undecided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := summarizeEmailAdapterOutcome(tt.providerMessageID, tt.failures, tt.fallback)

			require.Equal(t, tt.wantAccepted, outcome.accepted)
			require.Equal(t, tt.wantProviderID, outcome.providerMessageID)
			require.Equal(t, tt.wantStatus, outcome.failureStatus)
			require.Equal(t, tt.wantErrorType, outcome.failureErrorType)
			if tt.wantError == "" {
				require.NoError(t, outcome.failureErr)
			} else {
				require.EqualError(t, outcome.failureErr, tt.wantError)
			}
		})
	}
}
