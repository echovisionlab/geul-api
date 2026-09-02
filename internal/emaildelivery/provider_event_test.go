package emaildelivery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type providerOutcomeStoreStub struct {
	event  SESProviderOutcomeEvent
	result SESProviderOutcomeResult
	calls  int
}

func (s *providerOutcomeStoreStub) ApplySESProviderOutcome(
	_ context.Context,
	event SESProviderOutcomeEvent,
) (SESProviderOutcomeResult, error) {
	s.event = event
	s.calls++
	if s.result.UpdatedRecipients == 0 && len(s.result.MatchedRecipientEmails) == 0 {
		s.result.UpdatedRecipients = 1
	}
	return s.result, nil
}

type suppressionWriterStub struct{ requests []SuppressionRequest }

func (s *suppressionWriterStub) Suppress(_ context.Context, request SuppressionRequest) error {
	s.requests = append(s.requests, request)
	return nil
}

func TestApplySESProviderOutcomeNormalizesValidatedEventForCampaignPort(t *testing.T) {
	store := &providerOutcomeStoreStub{}
	eventAt := time.Date(2026, 8, 23, 1, 2, 3, 0, time.FixedZone("test", 9*60*60))
	result, err := ApplySESProviderOutcome(
		t.Context(), store, " provider-message ", SESProviderOutcomeComplained, eventAt, " complaint ",
	)
	require.NoError(t, err)
	require.Equal(t, 1, result.UpdatedRecipients)
	require.Equal(t, "provider-message", store.event.ProviderMessageID)
	require.Equal(t, SESProviderOutcomeComplained, store.event.Outcome)
	require.Equal(t, eventAt.UTC(), store.event.EventAt)
	require.Equal(t, "complaint", store.event.ErrorType)
}

func TestApplySESProviderOutcomeRejectsInvalidBoundaryInput(t *testing.T) {
	_, err := ApplySESProviderOutcome(t.Context(), &providerOutcomeStoreStub{}, "", SESProviderOutcomeDelivered, time.Time{}, "")
	require.Error(t, err)
	_, err = ApplySESProviderOutcome(t.Context(), &providerOutcomeStoreStub{}, "message", SESProviderOutcome("unknown"), time.Time{}, "")
	require.Error(t, err)
	_, err = ApplySESProviderOutcome(t.Context(), nil, "message", SESProviderOutcomeDelivered, time.Time{}, "")
	require.Error(t, err)
}

func TestProviderNotificationProcessorAppliesPermanentBounceAndSuppression(t *testing.T) {
	outcomes := &providerOutcomeStoreStub{result: SESProviderOutcomeResult{
		MatchedRecipientEmails: []string{"matched@example.test"}, UpdatedRecipients: 1,
	}}
	suppressions := &suppressionWriterStub{}
	processor := NewProviderNotificationProcessor(outcomes, suppressions)
	eventAt := time.Date(2026, 8, 23, 2, 3, 4, 0, time.UTC)

	err := processor.ProcessProviderNotification(t.Context(), ProviderNotification{
		Type: "bounce", ProviderMessageID: "provider-1", OccurredAt: eventAt,
		Recipients: []string{"BOUNCED@example.test"}, Permanent: true,
		ErrorType: "bounce:general",
	})
	require.NoError(t, err)
	require.Equal(t, SESProviderOutcomeBounced, outcomes.event.Outcome)
	require.Equal(t, []SuppressionRequest{
		{Email: "bounced@example.test", Reason: EmailSuppressionReasonSESBounce, Source: EmailSuppressionSourceSESCallback, ReferenceID: "provider-1", ErrorType: "bounce:general"},
		{Email: "matched@example.test", Reason: EmailSuppressionReasonSESBounce, Source: EmailSuppressionSourceSESCallback, ReferenceID: "provider-1", ErrorType: "bounce:general"},
	}, suppressions.requests)
}

func TestProviderNotificationProcessorIgnoresTransientBounce(t *testing.T) {
	outcomes := &providerOutcomeStoreStub{}
	suppressions := &suppressionWriterStub{}
	processor := NewProviderNotificationProcessor(outcomes, suppressions)

	require.NoError(t, processor.ProcessProviderNotification(t.Context(), ProviderNotification{
		Type: "bounce", ProviderMessageID: "provider-2", OccurredAt: time.Now().UTC(),
	}))
	require.Zero(t, outcomes.calls)
	require.Empty(t, suppressions.requests)
}
