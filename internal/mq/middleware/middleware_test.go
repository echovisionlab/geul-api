package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRecoveryReturnsSanitizedPanicWithoutLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	body, marshalErr := proto.Marshal(&managev1.SendEmailEvent{Recipient: "person@example.test", TemplateData: map[string]string{"code": "829173", "name": "Private Person"}})
	require.NoError(t, marshalErr)
	handler := Recovery()(func(context.Context, mq.Message) error {
		panic("boom person@example.test code 829173 for Private Person")
	})

	err := handler(context.Background(), mq.Message{Queue: eventpkg.QueueEmailAuth, Body: body, MessageID: "message-1", CorrelationID: "command-1"})
	require.Error(t, err)
	// The queue consumer logs the returned failure once.
	require.Empty(t, output.String())
	failure := failurecause.Of(err)
	require.Contains(t, failure.Cause, "queue handler panic: boom")
	for _, private := range []string{"person@example.test", "829173", "Private Person"} {
		require.NotContains(t, err.Error(), private)
		require.NotContains(t, failure.Cause, private)
	}
	var stack string
	for _, attr := range failure.Attrs {
		if attr.Key == "panic_stack" {
			stack = attr.Value.String()
		}
	}
	require.Contains(t, stack, "TestRecoveryReturnsSanitizedPanicWithoutLogging")
}

func TestRecoveryPassesSuccessfulHandler(t *testing.T) {
	called := false
	handler := Recovery()(func(context.Context, mq.Message) error {
		called = true
		return nil
	})

	require.NoError(t, handler(context.Background(), mq.Message{Queue: "jobs"}))
	assert.True(t, called)
}
