package mq

import (
	"context"
	"testing"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestPGMQHeaderCarrier(t *testing.T) {
	t.Parallel()

	carrier := pgmqHeaderCarrier{"existing": "value"}
	carrier.Set("added", "new-value")

	require.Equal(t, "value", carrier.Get("existing"))
	require.Equal(t, "new-value", carrier.Get("added"))
	require.Empty(t, carrier.Get("missing"))
	require.ElementsMatch(t, []string{"existing", "added"}, carrier.Keys())
}

func TestMessageCorrelationInjectionAndExtraction(t *testing.T) {
	t.Parallel()

	const requestID = "018f47a2-8a3d-4e17-9d42-6f12c89b1234"
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		requestID,
		sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceTranscoder},
	)
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(context.Background(), requestContext)

	for _, test := range []struct {
		name    string
		headers map[string]string
	}{
		{name: "nil headers"},
		{name: "existing headers", headers: map[string]string{"message-type": "test.Command"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := injectMessageCorrelation(ctx, test.headers)
			require.Equal(t, requestID, headers[sharedtelemetry.MessageRequestIDHeader])
			if test.headers != nil {
				require.Equal(t, "test.Command", headers["message-type"])
			}

			extracted := extractDeliveryCorrelation(
				context.Background(),
				eventpkg.Message{Headers: headers},
				sharedtelemetry.ServiceTranscoder,
			)
			got, ok := sharedtelemetry.RequestContextFrom(extracted)
			require.True(t, ok)
			require.Equal(t, requestID, got.RequestID)
			require.Equal(t, sharedtelemetry.ActorKindSystem, got.Actor.Kind())
		})
	}
}
