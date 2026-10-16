package emaildelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type providerOutcomeStoreStub struct {
	event  SESProviderOutcomeEvent
	result SESProviderOutcomeResult
	calls  int
	err    error
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
	return s.result, s.err
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

func TestProviderNotificationFailureLogsCauseAndProviderCorrelation(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	failure := errors.New(`write outcome: relation "email_delivery_recipient" missing for reader@example.test password=private-secret`)
	processor := NewProviderNotificationProcessor(&providerOutcomeStoreStub{err: failure}, &suppressionWriterStub{})
	err := processor.ProcessProviderNotification(t.Context(), ProviderNotification{
		Type: "delivery", ProviderMessageID: "provider-1", OccurredAt: time.Now().UTC(),
	})
	require.ErrorIs(t, err, failure)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "provider-1", entry["provider_message_id"])
	require.Equal(t, "delivered", entry["provider_outcome"])
	require.Contains(t, entry["failure_cause"], `relation "email_delivery_recipient" missing`)
	require.NotContains(t, output.String(), "reader@example.test")
	require.NotContains(t, output.String(), "private-secret")
}
