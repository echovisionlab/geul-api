package mq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestTransientProviderConfigurationFailureSchedulesQueueRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT msg_id FROM pgmq\.set_vt\(\$1, \$2::bigint, \$3::integer\)`).
		WithArgs("test.queue", int64(9), 5).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(int64(9)))

	consumer := NewQueueConsumer(db, QueueConfig{
		Name:         "test.queue",
		MessageType:  "api.test.Message",
		MaxRetries:   3,
		RetryDelay:   5 * time.Second,
		RetryBackoff: 1,
	}, func(context.Context, Message) error {
		return errors.New("temporary provider configuration database failure")
	})
	consumer.processMessage(context.Background(), queueOutcomeDelivery(t, 9, 1))
	require.NoError(t, mock.ExpectationsWereMet(), "a transient resolver failure must remain retryable at the queue boundary")
}

func TestWorkerShutdownReturnsDeliveryForRedelivery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT msg_id FROM pgmq\.set_vt\(\$1, \$2::bigint, \$3::integer\)`).
		WithArgs("test.queue", int64(8), 0).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(int64(8)))

	parent, stop := context.WithCancel(context.Background())
	stop()
	consumer := NewQueueConsumer(db, QueueConfig{
		Name:        "test.queue",
		MessageType: "api.test.Message",
		MaxRetries:  3,
	}, func(context.Context, Message) error {
		return errors.New("handler context stopped")
	})
	consumer.processMessage(parent, queueOutcomeDelivery(t, 8, 1))
	require.NoError(t, mock.ExpectationsWereMet(), "shutdown must restore visibility immediately for a later delivery")
}

func queueOutcomeDelivery(t *testing.T, id int64, readCount int) eventpkg.Message {
	t.Helper()
	envelope, err := eventpkg.NewEnvelope("stable-message", "api.test.Message", nil)
	require.NoError(t, err)
	return eventpkg.Message{TransportID: id, ReadCount: readCount, Envelope: envelope}
}
