package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRecoveryConvertsPanicToError(t *testing.T) {
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
	assert.Equal(t, "queue handler panic", err.Error())
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "boom")
	require.NotContains(t, output.String(), "person@example.test")
	require.NotContains(t, output.String(), "829173")
	require.NotContains(t, output.String(), "Private Person")
	require.Contains(t, entry["panic_stack"], "TestRecoveryConvertsPanicToError")
	require.Equal(t, "message-1", entry["message_id"])
	require.Equal(t, "command-1", entry["command_id"])
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
