package emaildelivery

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type deliveryCampaignStub struct {
	needs bool
	marks [][4]string
}

func (s *deliveryCampaignStub) NeedsDelivery(context.Context, string) (bool, error) {
	return s.needs, nil
}
func (s *deliveryCampaignStub) MarkResult(_ context.Context, id, status, providerID, errorType string) error {
	s.marks = append(s.marks, [4]string{id, status, providerID, errorType})
	return nil
}

type recipientAuthorizerStub struct{ decision RecipientDecision }

func (s recipientAuthorizerStub) Authorize(context.Context, *managev1.SendEmailEvent) (RecipientDecision, error) {
	return s.decision, nil
}

type deliveryRendererStub struct{ rendered *email.RenderedEmail }

func (s deliveryRendererStub) Render(context.Context, *managev1.SendEmailEvent) (*email.RenderedEmail, error) {
	return s.rendered, nil
}

type deliverySuppressionStub struct{ suppression *Suppression }

func (s deliverySuppressionStub) Find(context.Context, string) (*Suppression, error) {
	return s.suppression, nil
}
func (deliverySuppressionStub) Suppress(context.Context, SuppressionRequest) error { return nil }

type providerLoaderStub struct{ adapters []email.Adapter }

func (s providerLoaderStub) GetActiveAdapters(context.Context) ([]email.Adapter, error) {
	return s.adapters, nil
}

type deliveryAdapterStub struct{ messageID string }

func (s deliveryAdapterStub) Send(context.Context, *email.Email) (*email.SendResult, error) {
	return &email.SendResult{MessageID: s.messageID}, nil
}
func (deliveryAdapterStub) ID() string                  { return "adapter-1" }
func (deliveryAdapterStub) Name() string                { return "Adapter" }
func (deliveryAdapterStub) Type() model.MailAdapterType { return "smtp" }

type deliveryMetricsStub struct{}

func (deliveryMetricsStub) RecordSendAttempt(context.Context, string)             {}
func (deliveryMetricsStub) RecordSendResult(context.Context, string, string)      {}
func (deliveryMetricsStub) RecordRecipientStatus(context.Context, string, string) {}

func TestDeliveryApplicationAcceptsFirstProviderResult(t *testing.T) {
	campaign := &deliveryCampaignStub{needs: true}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		providerLoaderStub{adapters: []email.Adapter{deliveryAdapterStub{messageID: "provider-1"}}},
		deliveryMetricsStub{},
	)
	messageID := "command-1"
	recipientID := "recipient-1"
	result, err := application.Deliver(t.Context(), &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	})
	require.NoError(t, err)
	require.Equal(t, DeliveryAccepted, result.Outcome)
	require.Equal(t, [][4]string{{recipientID, RecipientStatusSent, "provider-1", ""}}, campaign.marks)
}

func TestDeliveryApplicationPersistsRecipientBlockBeforeProvider(t *testing.T) {
	campaign := &deliveryCampaignStub{needs: true}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{decision: RecipientDecision{Blocked: true, Reason: "identity_inactive"}},
		deliveryRendererStub{}, deliverySuppressionStub{}, providerLoaderStub{}, deliveryMetricsStub{},
	)
	messageID := "command-2"
	recipientID := "recipient-2"
	result, err := application.Deliver(t.Context(), &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	})
	require.NoError(t, err)
	require.Equal(t, DeliveryRecipientBlocked, result.Outcome)
	require.Equal(t, [][4]string{{recipientID, RecipientStatusBlocked, "", "identity_inactive"}}, campaign.marks)
}
