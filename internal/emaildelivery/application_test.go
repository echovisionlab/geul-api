package emaildelivery

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type deliveryCampaignStub struct {
	mu               sync.Mutex
	needs            bool
	claimAvailable   bool
	claimID          string
	terminal         bool
	markClaimedError error
	marks            [][4]string
	claimedMarks     [][5]string
	releases         [][2]string
}

func (s *deliveryCampaignStub) NeedsDelivery(context.Context, string) (bool, error) {
	return s.needs, nil
}
func (s *deliveryCampaignStub) MarkResult(_ context.Context, id, status, providerID, errorType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = append(s.marks, [4]string{id, status, providerID, errorType})
	return nil
}
func (s *deliveryCampaignStub) ClaimDelivery(context.Context, string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || !s.claimAvailable {
		return "", false, nil
	}
	s.claimAvailable = false
	return s.claimID, true, nil
}
func (s *deliveryCampaignStub) ReleaseDeliveryClaim(ctx context.Context, id, claimID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, [2]string{id, claimID})
	if err := ctx.Err(); err != nil {
		return err
	}
	s.claimAvailable = true
	return nil
}
func (s *deliveryCampaignStub) MarkClaimedResult(_ context.Context, id, claimID, status, providerID, errorType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimedMarks = append(s.claimedMarks, [5]string{id, claimID, status, providerID, errorType})
	if s.markClaimedError != nil {
		return s.markClaimedError
	}
	s.terminal = true
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

type deliveryAdapterStub struct {
	messageID string
	sendErr   error
	sends     *int
	mu        *sync.Mutex
}

func (s deliveryAdapterStub) Send(context.Context, *email.Email) (*email.SendResult, error) {
	if s.sends != nil {
		s.mu.Lock()
		*s.sends = *s.sends + 1
		s.mu.Unlock()
	}
	if s.sendErr != nil {
		return nil, s.sendErr
	}
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
	campaign := &deliveryCampaignStub{needs: true, claimAvailable: true, claimID: "claim-1"}
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
	require.Empty(t, campaign.marks)
	require.Equal(t, [][5]string{{recipientID, "claim-1", RecipientStatusSent, "provider-1", ""}}, campaign.claimedMarks)
	require.Empty(t, campaign.releases)
}

func TestDeliveryApplicationPreservesNonCampaignMailPath(t *testing.T) {
	campaign := &deliveryCampaignStub{needs: true}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		providerLoaderStub{adapters: []email.Adapter{deliveryAdapterStub{messageID: "provider-1"}}},
		deliveryMetricsStub{},
	)
	messageID := "message-1"
	result, err := application.Deliver(t.Context(), &managev1.SendEmailEvent{
		MessageId: &messageID, Recipient: "reader@example.test", TemplateType: "account:welcome",
	})
	require.NoError(t, err)
	require.Equal(t, DeliveryAccepted, result.Outcome)
	require.Empty(t, campaign.claimedMarks)
	require.Equal(t, [][4]string{{"", RecipientStatusSent, "provider-1", ""}}, campaign.marks)
}

func TestDeliveryApplicationPersistsRecipientBlockBeforeProvider(t *testing.T) {
	campaign := &deliveryCampaignStub{needs: true, claimAvailable: true, claimID: "claim-1"}
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
	require.Empty(t, campaign.marks)
	require.Equal(t, [][5]string{{recipientID, "claim-1", RecipientStatusBlocked, "", "identity_inactive"}}, campaign.claimedMarks)
}

func TestDeliveryApplicationConcurrentDuplicateSendsAcquireOnlyOneClaim(t *testing.T) {
	campaign := &deliveryCampaignStub{claimAvailable: true, claimID: "claim-1"}
	sends := 0
	var sendsMu sync.Mutex
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		providerLoaderStub{adapters: []email.Adapter{deliveryAdapterStub{
			messageID: "provider-1", sends: &sends, mu: &sendsMu,
		}}},
		deliveryMetricsStub{},
	)
	messageID, recipientID := "command-1", "recipient-1"
	job := &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := application.Deliver(t.Context(), job)
			results <- err
		}()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Equal(t, 1, sends)
	require.Len(t, campaign.claimedMarks, 1)
}

func TestDeliveryApplicationKeepsClaimWhenAcceptedResultCannotBePersisted(t *testing.T) {
	campaign := &deliveryCampaignStub{
		claimAvailable: true, claimID: "claim-1", markClaimedError: errors.New("database unavailable"),
	}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		providerLoaderStub{adapters: []email.Adapter{deliveryAdapterStub{messageID: "provider-1"}}},
		deliveryMetricsStub{},
	)
	messageID, recipientID := "command-1", "recipient-1"
	_, err := application.Deliver(t.Context(), &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	})
	require.ErrorContains(t, err, "database unavailable")
	require.Empty(t, campaign.releases)
}

type errorProviderLoader struct{ err error }

func (s errorProviderLoader) GetActiveAdapters(context.Context) ([]email.Adapter, error) {
	return nil, s.err
}

func TestDeliveryApplicationReleasesRetryableClaimWithLiveBoundedContext(t *testing.T) {
	campaign := &deliveryCampaignStub{claimAvailable: true, claimID: "claim-1"}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		errorProviderLoader{err: errors.New("temporary provider config failure")},
		deliveryMetricsStub{},
	)
	messageID, recipientID := "command-1", "recipient-1"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := application.Deliver(ctx, &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	})
	require.ErrorContains(t, err, "temporary provider config failure")
	require.Equal(t, [][2]string{{recipientID, "claim-1"}}, campaign.releases)
	require.True(t, campaign.claimAvailable, "the claim was released with a live context")
}

func TestDeliveryApplicationReleasesClaimAfterRetryableProviderFailure(t *testing.T) {
	campaign := &deliveryCampaignStub{claimAvailable: true, claimID: "claim-1"}
	application := NewDeliveryApplication(
		campaign,
		recipientAuthorizerStub{},
		deliveryRendererStub{rendered: &email.RenderedEmail{Subject: "subject", HTML: "<p>body</p>"}},
		deliverySuppressionStub{},
		providerLoaderStub{adapters: []email.Adapter{deliveryAdapterStub{
			sendErr: email.NewDeliveryError(email.DeliveryErrorConnection, true, errors.New("temporary network failure")),
		}}},
		deliveryMetricsStub{},
	)
	messageID, recipientID := "command-1", "recipient-1"
	result, err := application.Deliver(t.Context(), &managev1.SendEmailEvent{
		MessageId: &messageID, DeliveryRecipientId: &recipientID,
		Recipient: "reader@example.test", TemplateType: "campaign:test",
	})
	require.NoError(t, err)
	require.True(t, result.Retryable)
	require.ErrorContains(t, result.Err, "temporary network failure")
	require.Equal(t, [][2]string{{recipientID, "claim-1"}}, campaign.releases)
	require.Empty(t, campaign.claimedMarks)
}
