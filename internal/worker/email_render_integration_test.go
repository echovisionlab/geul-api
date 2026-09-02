//go:build integration

package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestTransactionalEmailUsesNoFenceAndStopsAfterProviderAcceptanceIntegration(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	commandID := "transactional-provider-" + uuid.NewString()
	referenceID := "identity-" + uuid.NewString()
	primary := &providerMessageIDEmailAdapter{
		messageID:   "provider-primary-" + uuid.NewString(),
		adapterType: model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES.String()),
	}
	failover := &providerMessageIDEmailAdapter{
		messageID:   "provider-failover-" + uuid.NewString(),
		adapterType: model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String()),
	}
	handlers := &Handlers{
		db: db,
		config: &config.Config{
			CDNURL:             "https://cdn.example.test",
			SiteOrigin:         "https://www.example.test",
			TokenSigningSecret: "transactional-email-test-key-at-least-32-bytes",
		},
		adapterLoader: staticDurableEmailAdapterLoader{primary, failover},
	}
	job := &managev1.SendEmailEvent{
		Recipient:        "transactional@example.test",
		TemplateType:     email.EventAccountDeletionComplete.String(),
		TemplateData:     map[string]string{"name": "John Doe"},
		RecipientContext: email.SystemDirectEmailContext("account_deletion_complete"),
		MessageId:        &commandID,
		ReferenceId:      &referenceID,
	}

	require.NoError(t, handlers.handleSendEmail(t.Context(), job))
	require.Equal(t, 1, primary.sendCount)
	require.Zero(t, failover.sendCount, "provider acceptance must stop adapter failover")

	var fenceName *string
	require.NoError(t, db.Raw("SELECT to_regclass('public.auth_email_command_fence')").Scan(&fenceName).Error)
	require.Nil(t, fenceName)
}

func TestTransactionalTransientProviderFailureRemainsQueueRetryableIntegration(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	commandID := "transactional-retry-" + uuid.NewString()
	referenceID := "identity-" + uuid.NewString()
	adapter := &providerMessageIDEmailAdapter{
		err: email.NewDeliveryError(
			email.DeliveryErrorRateLimited,
			true,
			errors.New("provider throttled"),
		),
	}
	handlers := &Handlers{
		db: db,
		config: &config.Config{
			CDNURL:     "https://cdn.example.test",
			SiteOrigin: "https://www.example.test",
		},
		adapterLoader: staticDurableEmailAdapterLoader{adapter},
	}
	job := &managev1.SendEmailEvent{
		Recipient:        "retry@example.test",
		TemplateType:     email.EventAccountDeletionComplete.String(),
		TemplateData:     map[string]string{"name": "John Doe"},
		RecipientContext: email.SystemDirectEmailContext("account_deletion_complete"),
		MessageId:        &commandID,
		ReferenceId:      &referenceID,
	}

	err := handlers.handleSendEmail(t.Context(), job)
	require.ErrorContains(t, err, "mail delivery retry requested")
	require.Equal(t, 1, adapter.sendCount)
}

func TestTransactionalPermanentProviderFailureReturnsTerminalQueueDecisionIntegration(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	commandID := "transactional-terminal-" + uuid.NewString()
	referenceID := "identity-" + uuid.NewString()
	providerErr := errors.New("provider rejected recipient")
	adapter := &providerMessageIDEmailAdapter{
		err: email.NewDeliveryError(
			email.DeliveryErrorInvalidRecipient,
			false,
			providerErr,
		),
	}
	handlers := &Handlers{
		db: db,
		config: &config.Config{
			CDNURL:     "https://cdn.example.test",
			SiteOrigin: "https://www.example.test",
		},
		adapterLoader: staticDurableEmailAdapterLoader{adapter},
	}
	job := &managev1.SendEmailEvent{
		Recipient:        "terminal@example.test",
		TemplateType:     email.EventAccountDeletionComplete.String(),
		TemplateData:     map[string]string{"name": "John Doe"},
		RecipientContext: email.SystemDirectEmailContext("account_deletion_complete"),
		MessageId:        &commandID,
		ReferenceId:      &referenceID,
	}

	err := handlers.handleSendEmail(t.Context(), job)

	require.ErrorIs(t, err, providerErr)
	errorClass, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "email_invalid_recipient", errorClass)
	require.Equal(t, 1, adapter.sendCount)
}

type providerMessageIDEmailAdapter struct {
	messageID   string
	adapterType model.MailAdapterType
	err         error
	sendCount   int
}

func (a *providerMessageIDEmailAdapter) ID() string   { return "provider-message-id-adapter" }
func (a *providerMessageIDEmailAdapter) Name() string { return "Provider message ID adapter" }
func (a *providerMessageIDEmailAdapter) Type() model.MailAdapterType {
	if a.adapterType != "" {
		return a.adapterType
	}
	return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String())
}
func (a *providerMessageIDEmailAdapter) Send(context.Context, *email.Email) (*email.SendResult, error) {
	a.sendCount++
	if a.err != nil {
		return nil, a.err
	}
	return &email.SendResult{MessageID: a.messageID}, nil
}

type staticDurableEmailAdapterLoader []email.Adapter

func (l staticDurableEmailAdapterLoader) GetActiveAdapters(context.Context) ([]email.Adapter, error) {
	return []email.Adapter(l), nil
}
