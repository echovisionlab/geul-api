package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/smithy-go"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// emailRegex is a simple regex for basic email validation.
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// SESAdapter sends emails using AWS SES.
type SESAdapter struct {
	id     string
	name   string
	client sesEmailClient
	from   string
}

type sesEmailClient interface {
	SendEmail(context.Context, *ses.SendEmailInput, ...func(*ses.Options)) (*ses.SendEmailOutput, error)
}

// NewSESAdapter creates a new SESAdapter from configuration.
func NewSESAdapter(id, name string, cfg *model.SESAdapterConfig) (*SESAdapter, error) {
	// Validate from email format
	if cfg.FromEmail == "" {
		return nil, fmt.Errorf("SES from email is required")
	}
	if !emailRegex.MatchString(cfg.FromEmail) {
		return nil, fmt.Errorf("invalid SES from email format: %s", cfg.FromEmail)
	}

	// Format the optional display name using RFC 5322 and RFC 2047 rules.
	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = (&mail.Address{Name: cfg.FromName, Address: cfg.FromEmail}).String()
	}

	// Create AWS config
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID,
			cfg.SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS config: %w", err)
	}

	client := ses.NewFromConfig(awsCfg)

	return &SESAdapter{
		id:     id,
		name:   name,
		client: client,
		from:   from,
	}, nil
}

// ID returns the adapter ID.
func (a *SESAdapter) ID() string { return a.id }

// Name returns the adapter name.
func (a *SESAdapter) Name() string { return a.name }

// Type returns the adapter type.
func (a *SESAdapter) Type() model.MailAdapterType {
	return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String())
}

// Send sends an email using AWS SES.
func (a *SESAdapter) Send(ctx context.Context, email *Email) (*SendResult, error) {
	input := &ses.SendEmailInput{
		Source: aws.String(a.from),
		Destination: &types.Destination{
			ToAddresses: []string{email.To},
		},
		Message: &types.Message{
			Subject: &types.Content{
				Data:    aws.String(email.Subject),
				Charset: aws.String("UTF-8"),
			},
			Body: &types.Body{},
		},
	}

	if email.HTML != "" {
		input.Message.Body.Html = &types.Content{
			Data:    aws.String(email.HTML),
			Charset: aws.String("UTF-8"),
		}
	}

	if email.Text != "" {
		input.Message.Body.Text = &types.Content{
			Data:    aws.String(email.Text),
			Charset: aws.String("UTF-8"),
		}
	}

	output, err := a.client.SendEmail(ctx, input)
	if err != nil {
		logAdapterLifecycle(
			ctx,
			slog.LevelError,
			"Email provider request failed",
			"mail.provider.failed",
			a.id,
			a.name,
			email,
			"",
			"failed",
			"provider_request_failed",
			"provider_error",
		)
		return nil, classifySESDeliveryError(err)
	}

	if output == nil {
		err := NewDeliveryError(
			DeliveryErrorUnknown,
			false,
			fmt.Errorf("SES SendEmail returned nil output"),
		)
		logAdapterLifecycle(ctx, slog.LevelError, "Email provider response was invalid", "mail.provider.failed", a.id, a.name, email, "", "failed", "missing_provider_response", "provider_response_invalid")
		return nil, err
	}
	if output.MessageId == nil {
		err := NewDeliveryError(
			DeliveryErrorUnknown,
			false,
			fmt.Errorf("SES SendEmail returned nil MessageId"),
		)
		logAdapterLifecycle(ctx, slog.LevelError, "Email provider response was invalid", "mail.provider.failed", a.id, a.name, email, "", "failed", "missing_provider_message_id", "provider_response_invalid")
		return nil, err
	}
	messageID := strings.TrimSpace(*output.MessageId)
	if messageID == "" {
		err := NewDeliveryError(
			DeliveryErrorUnknown,
			false,
			fmt.Errorf("SES SendEmail returned empty MessageId"),
		)
		logAdapterLifecycle(ctx, slog.LevelError, "Email provider response was invalid", "mail.provider.failed", a.id, a.name, email, "", "failed", "missing_provider_message_id", "provider_response_invalid")
		return nil, err
	}
	logAdapterLifecycle(
		ctx,
		slog.LevelInfo,
		"Email accepted by provider",
		"mail.provider.accepted",
		a.id,
		a.name,
		email,
		messageID,
		"accepted",
		"",
		"",
	)
	return &SendResult{MessageID: messageID}, nil
}

func classifySESDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}

	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return NewDeliveryError(DeliveryErrorUnknown, false, err)
	}
	switch apiErr.ErrorCode() {
	case "Throttling", "ThrottlingException", "TooManyRequestsException":
		return NewDeliveryError(DeliveryErrorRateLimited, true, err)
	case "ServiceUnavailable", "ServiceUnavailableException", "InternalFailure", "InternalError", "RequestTimeout":
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	case "MessageRejected":
		// SES uses MessageRejected for several message-level policy failures
		// (content, account policy, identity policy, and other rejection paths).
		// It is not authoritative evidence that the destination mailbox is
		// invalid, so it must never create a recipient suppression record.
		return NewDeliveryError(DeliveryErrorUnknown, false, err)
	case "MailFromDomainNotVerifiedException",
		"ConfigurationSetDoesNotExist",
		"ConfigurationSetSendingPausedException",
		"AccountSendingPausedException":
		return NewDeliveryError(DeliveryErrorAuthentication, false, err)
	default:
		return NewDeliveryError(DeliveryErrorUnknown, false, err)
	}
}
