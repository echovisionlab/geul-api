package mq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestQueueHandlerCauseSurvivesEveryFailedHandoff(t *testing.T) {
	for _, test := range []struct {
		name                              string
		terminal, exhausted, handoffFails bool
	}{
		{name: "retry"},
		{name: "terminal", terminal: true},
		{name: "exhausted", exhausted: true},
		{name: "retry handoff fails", handoffFails: true},
		{name: "archive handoff fails", terminal: true, handoffFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			query := `SELECT msg_id FROM pgmq\.set_vt\(\$1, \$2::bigint, \$3::integer\)`
			if test.terminal || test.exhausted {
				query = `SELECT pgmq\.archive\(\$1, \$2::bigint\)`
			}
			expect := mock.ExpectQuery(query)
			if test.handoffFails {
				expect.WillReturnError(errors.New("handoff database unavailable"))
			} else if test.terminal || test.exhausted {
				expect.WillReturnRows(sqlmock.NewRows([]string{"archive"}).AddRow(true))
			} else {
				expect.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(int64(9)))
			}
			maxRetries := 3
			if test.exhausted {
				maxRetries = 0
			}
			consumer := NewQueueConsumer(db, QueueConfig{
				Name: "test.queue", MessageType: "api.test.Message", MaxRetries: maxRetries, RetryDelay: time.Second,
			}, func(context.Context, Message) error {
				cause := fmt.Errorf("execute job: %w", errors.New("storage unavailable for person@example.test"))
				if test.terminal {
					return NewTerminalDeliveryError("invalid_job", cause)
				}
				return cause
			})
			delivery := queueOutcomeDelivery(t, 9, 1)
			delivery.Envelope.CorrelationID = "command-1"
			consumer.processMessage(t.Context(), delivery)
			require.NoError(t, mock.ExpectationsWereMet())
			var failure map[string]any
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var entry map[string]any
				require.NoError(t, json.Unmarshal(line, &entry))
				if entry["msg"] == "PGMQ handler failed" {
					failure = entry
				}
			}
			require.NotNil(t, failure)
			require.Contains(t, failure["failure_cause"], "execute job: storage unavailable")
			require.Equal(t, "test.queue", failure["queue"])
			require.Equal(t, "stable-message", failure["message_id"])
			require.Equal(t, "command-1", failure["command_id"])
			require.Equal(t, float64(0), failure["retry_count"])
			require.NotContains(t, output.String(), "person@example.test")
			if test.handoffFails {
				require.Contains(t, output.String(), "handoff database unavailable")
			}
		})
	}
}

func TestInvalidPayloadArchiveFailureIsLogged(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT pgmq\.archive\(\$1, \$2::bigint\)`).WillReturnError(errors.New("archive write failed"))
	consumer := NewQueueConsumer(db, QueueConfig{Name: "test.queue", MessageType: "api.test.Message"}, nil)
	delivery := queueOutcomeDelivery(t, 9, 1)
	delivery.Envelope.PayloadBase64 = "%%%"
	consumer.processMessage(t.Context(), delivery)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Contains(t, output.String(), "archive write failed")
	require.Contains(t, output.String(), "Invalid PGMQ envelope payload")
}

func TestQueueEmailFailureRedactsTemplateValues(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT msg_id FROM pgmq\.set_vt\(\$1, \$2::bigint, \$3::integer\)`).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(int64(9)))
	body, err := proto.Marshal(&managev1.SendEmailEvent{Recipient: "person@example.test", TemplateData: map[string]string{"code": "829173", "name": "Private Person"}})
	require.NoError(t, err)
	consumer := NewQueueConsumer(db, QueueConfig{Name: "email.auth", MessageType: "api.manage.v1.SendEmailEvent", MaxRetries: 3}, func(context.Context, Message) error {
		return errors.New("provider rejected code 829173 for Private Person person@example.test")
	})
	delivery := queueOutcomeDelivery(t, 9, 1)
	envelope, err := eventpkg.NewEnvelope("email-message", "api.manage.v1.SendEmailEvent", body)
	require.NoError(t, err)
	delivery.Envelope = envelope
	consumer.processMessage(t.Context(), delivery)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Contains(t, output.String(), "provider rejected")
	for _, private := range []string{"829173", "Private Person", "person@example.test"} {
		require.NotContains(t, output.String(), private)
	}
}

func TestQueuePublishFailureRetainsCause(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	emitQueuePublishResult(t.Context(), "email.auth", "command-1", time.Millisecond, sharedtelemetry.QueueFailureEnqueueFailed, errors.New(`relation "pgmq.q_email_auth" does not exist for person@example.test`))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "queue.publish.failed", entry["event"])
	require.Equal(t, "command-1", entry["message_id"])
	require.Equal(t, "command-1", entry["command_id"])
	require.Contains(t, entry["failure_cause"], `relation "pgmq.q_email_auth" does not exist`)
	require.NotContains(t, output.String(), "person@example.test")
}

func TestEmailEnqueueFailureRedactsTemplateValues(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cause := errors.New("enqueue failed for Private Person with code 829173 at person@example.test")
	mock.ExpectQuery(`SELECT pgmq\.send\(`).WillReturnError(cause)
	job := &managev1.SendEmailEvent{Recipient: "person@example.test", TemplateData: map[string]string{"code": "829173", "name": "Private Person"}}
	enqueueErr := EnqueueProtobuf(t.Context(), db, eventpkg.QueueEmailAuth, "message-1", job)
	require.ErrorIs(t, enqueueErr, cause)
	require.NoError(t, mock.ExpectationsWereMet())
	// The caller's failure record redacts the same values from the returned error.
	for _, private := range []string{"829173", "Private Person", "person@example.test"} {
		require.NotContains(t, failurecause.Message(enqueueErr), private)
	}
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "enqueue failed")
	for _, private := range []string{"829173", "Private Person", "person@example.test"} {
		require.NotContains(t, output.String(), private)
	}
}
