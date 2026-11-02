package email

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"
)

type stubSESEmailClient struct {
	output *ses.SendEmailOutput
	err    error
	calls  int
}

func (c *stubSESEmailClient) SendEmail(
	context.Context,
	*ses.SendEmailInput,
	...func(*ses.Options),
) (*ses.SendEmailOutput, error) {
	c.calls++
	return c.output, c.err
}

func TestNewSESAdapterDoesNotProbeSES(t *testing.T) {
	adapter, err := NewSESAdapter("ses-test", "SES test", &SESAdapterConfig{
		Region:          "us-east-1",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		FromEmail:       "sender@example.test",
	})

	require.NoError(t, err)
	require.NotNil(t, adapter)
}

func TestNewSESAdapterFormatsSourceAddress(t *testing.T) {
	tests := []struct {
		name        string
		fromName    string
		exactSource string
	}{
		{
			name:        "address only",
			exactSource: "sender@example.test",
		},
		{
			name:        "ASCII display name",
			fromName:    "Geul Mail",
			exactSource: `"Geul Mail" <sender@example.test>`,
		},
		{
			name:     "Unicode display name",
			fromName: "Geul 메일",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewSESAdapter("ses-test", "SES test", &SESAdapterConfig{
				Region:          "us-east-1",
				AccessKeyID:     "test-access-key",
				SecretAccessKey: "test-secret-key",
				FromEmail:       "sender@example.test",
				FromName:        tt.fromName,
			})
			require.NoError(t, err)

			parsed, err := mail.ParseAddress(adapter.from)
			require.NoError(t, err)
			require.Equal(t, "sender@example.test", parsed.Address)
			require.Equal(t, tt.fromName, parsed.Name)

			if tt.exactSource != "" {
				require.Equal(t, tt.exactSource, adapter.from)
				return
			}
			require.NotContains(t, adapter.from, tt.fromName)
			require.True(t, strings.Contains(adapter.from, "=?utf-8?q?") || strings.Contains(adapter.from, "=?utf-8?b?"))
		})
	}
}

func TestSESAdapterSendAcceptsNonemptyProviderMessageID(t *testing.T) {
	logOutput := captureEmailPackageLogs(t)
	client := &stubSESEmailClient{
		output: &ses.SendEmailOutput{MessageId: aws.String("  ses-message-id  ")},
	}
	adapter := newTestSESAdapter(client)

	result, err := adapter.Send(t.Context(), sesTestEmail())

	require.NoError(t, err)
	require.Equal(t, "ses-message-id", result.MessageID)
	require.Equal(t, 1, client.calls)
	entry := decodeEmailPackageLog(t, logOutput)
	require.Equal(t, "mail.provider.accepted", entry["event"])
	require.Equal(t, "local-logical-message-id", entry["logical_message_id"])
	require.Equal(t, "ses-message-id", entry["provider_message_id"])
	require.NotContains(t, logOutput.String(), "recipient@example.test")
}

func TestSESAdapterSendReturnsProviderError(t *testing.T) {
	logOutput := captureEmailPackageLogs(t)
	providerErr := errors.New("SES rejected recipient@example.test")
	client := &stubSESEmailClient{err: providerErr}
	adapter := newTestSESAdapter(client)

	result, err := adapter.Send(t.Context(), sesTestEmail())

	require.Nil(t, result)
	require.ErrorIs(t, err, providerErr)
	kind, retryable, ok := DeliveryErrorDecision(err)
	require.True(t, ok)
	require.Equal(t, DeliveryErrorUnknown, kind)
	require.False(t, retryable)
	require.Equal(t, 1, client.calls)
	entry := decodeEmailPackageLog(t, logOutput)
	require.Equal(t, "mail.provider.failed", entry["event"])
	require.Equal(t, "provider_request_failed", entry["reason"])
	require.Contains(t, entry["failure_cause"], "SES rejected")
	require.NotContains(t, logOutput.String(), "recipient@example.test")
}

func TestSESAdapterSendUsesTypedProviderRetryPolicy(t *testing.T) {
	tests := []struct {
		code      string
		wantKind  DeliveryErrorKind
		retryable bool
	}{
		{code: "Throttling", wantKind: DeliveryErrorRateLimited, retryable: true},
		{code: "ServiceUnavailable", wantKind: DeliveryErrorConnection, retryable: true},
		{code: "MessageRejected", wantKind: DeliveryErrorUnknown, retryable: false},
		{code: "AccountSendingPausedException", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "ConfigurationSetDoesNotExist", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "ConfigurationSetSendingPausedException", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "MailFromDomainNotVerifiedException", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "InvalidClientTokenId", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "SignatureDoesNotMatch", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "AccessDeniedException", wantKind: DeliveryErrorAuthentication, retryable: false},
		{code: "UndocumentedProviderError", wantKind: DeliveryErrorUnknown, retryable: false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			adapter := newTestSESAdapter(&stubSESEmailClient{err: &smithy.GenericAPIError{Code: tt.code, Message: "provider failure"}})
			_, err := adapter.Send(t.Context(), sesTestEmail())
			kind, retryable, ok := DeliveryErrorDecision(err)
			require.True(t, ok)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.retryable, retryable)
		})
	}
}

func TestSESAdapterFailureKeepsCauseAndRedactsMessageValues(t *testing.T) {
	output := captureEmailPackageLogs(t)
	providerErr := fmt.Errorf("SES request failed: %w", errors.New(`element <img> missing {unit}; relation "mail_queue"; Private Recipient; Private Sender; private subject; token=provider-secret`))
	adapter := newTestSESAdapter(&stubSESEmailClient{err: providerErr})
	adapter.from = `Private Sender <sender@example.test>`
	message := sesTestEmail()
	message.To = `Private Recipient <recipient@example.test>`
	message.Subject = "private subject"
	_, err := adapter.Send(t.Context(), message)
	require.ErrorIs(t, err, providerErr)
	entry := decodeEmailPackageLog(t, output)
	require.Contains(t, entry["failure_cause"], `SES request failed: element <img> missing {unit}; relation "mail_queue"`)
	for _, private := range []string{"Private Recipient", "Private Sender", "private subject", "provider-secret", "sender@example.test", "recipient@example.test"} {
		require.NotContains(t, output.String(), private)
	}
}

func TestSESAdapterCancellationRemainsRetryable(t *testing.T) {
	for _, providerErr := range []error{context.Canceled, context.DeadlineExceeded} {
		classified := classifySESDeliveryError(providerErr)
		kind, retryable, ok := DeliveryErrorDecision(classified)
		require.True(t, ok)
		require.Equal(t, DeliveryErrorConnection, kind)
		require.True(t, retryable)
		require.ErrorIs(t, classified, providerErr)
	}
}

func TestSESAdapterSendRejectsMissingProviderMessageID(t *testing.T) {
	tests := []struct {
		name    string
		output  *ses.SendEmailOutput
		message string
	}{
		{
			name:    "nil output",
			message: "SES SendEmail returned nil output",
		},
		{
			name:    "nil message ID",
			output:  &ses.SendEmailOutput{},
			message: "SES SendEmail returned nil MessageId",
		},
		{
			name:    "empty message ID",
			output:  &ses.SendEmailOutput{MessageId: aws.String(" \t\n ")},
			message: "SES SendEmail returned empty MessageId",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &stubSESEmailClient{output: tt.output}
			adapter := newTestSESAdapter(client)

			result, err := adapter.Send(t.Context(), sesTestEmail())

			require.Nil(t, result)
			require.ErrorContains(t, err, tt.message)
			kind, retryable, ok := DeliveryErrorDecision(err)
			require.True(t, ok)
			require.Equal(t, DeliveryErrorUnknown, kind)
			require.False(t, retryable)
			require.Equal(t, 1, client.calls)
		})
	}
}

func newTestSESAdapter(client sesEmailClient) *SESAdapter {
	return &SESAdapter{
		id:     "ses-test",
		name:   "SES test",
		client: client,
		from:   "sender@example.test",
	}
}

func sesTestEmail() *Email {
	return &Email{
		MessageID: "local-logical-message-id",
		To:        "recipient@example.test",
		Subject:   "SES adapter test",
		Text:      "test",
	}
}
