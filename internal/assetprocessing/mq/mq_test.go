package mq

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/assetprocessing/jobs"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func mockConnection(t *testing.T) (*Connection, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &Connection{db: db}, mock
}

func queueConfig() jobs.QueueConfig {
	return jobs.QueueConfig{Name: eventpkg.QueueAssetOptimizerMesh, MessageType: "test.Command", Workers: 1, Timeout: time.Second, RetryLimit: 1}
}

func validMessage(t *testing.T, readCount int) eventpkg.Message {
	t.Helper()
	envelope, err := eventpkg.NewEnvelope("command-1", "test.Command", []byte("payload"))
	require.NoError(t, err)
	return eventpkg.Message{TransportID: 42, ReadCount: readCount, Envelope: envelope}
}

func expectBoolean(mock sqlmock.Sqlmock, operation string, result bool, err error) {
	query := regexp.QuoteMeta("SELECT pgmq." + operation + "($1, $2::bigint)")
	expectation := mock.ExpectQuery(query).WithArgs(eventpkg.QueueAssetOptimizerMesh, int64(42))
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow(result))
}

func expectRetry(mock sqlmock.Sqlmock, seconds int, err error) {
	expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT msg_id FROM pgmq.set_vt($1, $2::bigint, $3::integer)")).
		WithArgs(eventpkg.QueueAssetOptimizerMesh, int64(42), seconds)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(42))
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type retryableTestError struct{ error }

func (retryableTestError) Retryable() bool { return true }

type terminalTestError struct{ error }

func (terminalTestError) TerminalResult() bool { return true }

func TestConnectionLifecycle(t *testing.T) {
	require.ErrorContains(t, func() error { _, err := NewConnection(""); return err }(), "DSN")
	_, err := NewConnection("://invalid")
	require.Error(t, err)
	original := openPostgres
	t.Cleanup(func() { openPostgres = original })

	openPostgres = func(string) (*sql.DB, error) { return nil, errors.New("open") }
	_, err = NewConnection("postgres://test")
	require.ErrorContains(t, err, "open PostgreSQL")

	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	openPostgres = func(string) (*sql.DB, error) { return db, nil }
	mock.ExpectPing().WillReturnError(errors.New("offline"))
	mock.ExpectClose()
	_, err = NewConnection("postgres://test")
	require.ErrorContains(t, err, "connect to PostgreSQL")
	require.NoError(t, mock.ExpectationsWereMet())

	db, mock, err = sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	openPostgres = func(string) (*sql.DB, error) { return db, nil }
	mock.ExpectPing()
	connection, err := NewConnection("postgres://test")
	require.NoError(t, err)
	require.Same(t, db, connection.DB())
	require.False(t, connection.IsClosed())
	mock.ExpectPing()
	require.True(t, connection.Healthy())
	mock.ExpectPing().WillReturnError(errors.New("offline"))
	require.False(t, connection.Healthy())
	require.True(t, (*Connection)(nil).IsClosed())
	require.False(t, (*Connection)(nil).Healthy())
	mock.ExpectClose()
	require.NoError(t, connection.Close())
	require.True(t, connection.IsClosed())
	require.NoError(t, connection.Close())
	require.NoError(t, (*Connection)(nil).Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerValidationAndReadiness(t *testing.T) {
	config := queueConfig()
	_, err := NewConsumer(nil, config, func(context.Context, []byte) error { return nil })
	require.ErrorContains(t, err, "connection")
	connection, mock := mockConnection(t)
	connection.closed.Store(true)
	_, err = NewConsumer(connection, config, func(context.Context, []byte) error { return nil })
	require.Error(t, err)
	connection.closed.Store(false)
	_, err = NewConsumer(connection, jobs.QueueConfig{}, func(context.Context, []byte) error { return nil })
	require.ErrorContains(t, err, "configuration")

	consumer, err := NewConsumer(connection, config, func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT total_messages FROM pgmq.metrics($1)")).
		WithArgs(config.Name).WillReturnError(errors.New("denied"))
	require.ErrorContains(t, consumer.Start(context.Background()), "readiness")

	mock.ExpectQuery(regexp.QuoteMeta("SELECT total_messages FROM pgmq.metrics($1)")).
		WithArgs(config.Name).WillReturnRows(sqlmock.NewRows([]string{"total_messages"}).AddRow(0))
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, consumer.Start(ctx))
	cancel()
	require.NoError(t, consumer.Close())
	require.NoError(t, consumer.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerProcessingOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		readCount int
		parent    func() context.Context
		handler   error
		expect    func(sqlmock.Sqlmock)
	}{
		{name: "success", readCount: 1, parent: context.Background, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", true, nil) }},
		{name: "completion failure", readCount: 1, parent: context.Background, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", false, nil) }},
		{name: "cancelled", readCount: 1, parent: cancelledContext, handler: errors.New("cancelled"), expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 0, nil) }},
		{name: "retry", readCount: 1, parent: context.Background, handler: retryableTestError{errors.New("retry")}, expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 5, nil) }},
		{name: "retry failure", readCount: 1, parent: context.Background, handler: retryableTestError{errors.New("retry")}, expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 5, errors.New("retry failed")) }},
		{name: "terminal result", readCount: 1, parent: context.Background, handler: terminalTestError{errors.New("terminal")}, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", true, nil) }},
		{name: "terminal completion failure", readCount: 1, parent: context.Background, handler: terminalTestError{errors.New("terminal")}, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", false, nil) }},
		{name: "archive", readCount: 2, parent: context.Background, handler: errors.New("terminal"), expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "archive", true, nil) }},
		{name: "archive failure", readCount: 2, parent: context.Background, handler: errors.New("terminal"), expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "archive", false, nil) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, mock := mockConnection(t)
			consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return test.handler })
			require.NoError(t, err)
			test.expect(mock)
			consumer.process(test.parent(), validMessage(t, test.readCount))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}

	connection, mock := mockConnection(t)
	consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	expectBoolean(mock, "archive", true, nil)
	consumer.process(context.Background(), eventpkg.Message{TransportID: 42, Envelope: eventpkg.Envelope{}})
	require.NoError(t, mock.ExpectationsWereMet())

	connection, mock = mockConnection(t)
	consumer, err = NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { t.Fatal("handler called"); return nil })
	require.NoError(t, err)
	mismatched := validMessage(t, 1)
	mismatched.Envelope.MessageType = "other.Command"
	expectBoolean(mock, "archive", true, nil)
	consumer.process(context.Background(), mismatched)
	require.NoError(t, mock.ExpectationsWereMet())

	connection, mock = mockConnection(t)
	consumer, err = NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		t.Fatal("handler called for contract-invalid message")
		return nil
	})
	require.NoError(t, err)
	contractInvalid := validMessage(t, 1)
	contractInvalid.ContractError = "invalid headers"
	expectBoolean(mock, "archive", false, nil)
	consumer.process(context.Background(), contractInvalid)
	require.NoError(t, mock.ExpectationsWereMet())

	connection, mock = mockConnection(t)
	consumer, err = NewConsumer(connection, queueConfig(), func(context.Context, []byte) error {
		t.Fatal("handler called for invalid payload")
		return nil
	})
	require.NoError(t, err)
	invalidPayload := validMessage(t, 1)
	invalidPayload.Envelope.PayloadBase64 = "not-base64!"
	expectBoolean(mock, "archive", true, nil)
	consumer.process(context.Background(), invalidPayload)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerWorkerReadPaths(t *testing.T) {
	t.Run("cancel during read", func(t *testing.T) {
		connection, mock := mockConnection(t)
		consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
		require.NoError(t, err)
		mock.ExpectQuery("FROM pgmq.read").WillDelayFor(100 * time.Millisecond).WillReturnError(errors.New("read"))
		ctx, cancel := context.WithCancel(context.Background())
		consumer.wg.Add(1)
		done := make(chan struct{})
		go func() { consumer.worker(ctx, 0); close(done) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		<-done
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("read error", func(t *testing.T) {
		connection, mock := mockConnection(t)
		consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
		require.NoError(t, err)
		mock.ExpectQuery("FROM pgmq.read").WillReturnError(errors.New("read"))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		consumer.wg.Add(1)
		go func() { consumer.worker(ctx, 0); close(done) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		<-done
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("empty then close", func(t *testing.T) {
		connection, mock := mockConnection(t)
		consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
		require.NoError(t, err)
		mock.ExpectQuery("FROM pgmq.read").WillReturnRows(sqlmock.NewRows([]string{"msg_id", "read_ct", "enqueued_at", "vt", "message", "headers"}))
		done := make(chan struct{})
		consumer.wg.Add(1)
		go func() { consumer.worker(context.Background(), 0); close(done) }()
		time.Sleep(20 * time.Millisecond)
		close(consumer.done)
		<-done
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("message", func(t *testing.T) {
		connection, mock := mockConnection(t)
		consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
		require.NoError(t, err)
		message := validMessage(t, 1)
		envelopeJSON, err := json.Marshal(message.Envelope)
		require.NoError(t, err)
		now := time.Now()
		mock.ExpectQuery("FROM pgmq.read").WillReturnRows(sqlmock.NewRows([]string{"msg_id", "read_ct", "enqueued_at", "vt", "message", "headers"}).
			AddRow(message.TransportID, message.ReadCount, now, now, envelopeJSON, []byte("{}")))
		expectBoolean(mock, "delete", true, nil)
		ctx, cancel := context.WithCancel(context.Background())
		consumer.wg.Add(1)
		done := make(chan struct{})
		go func() { consumer.worker(ctx, 0); close(done) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		<-done
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestPublisherOperations(t *testing.T) {
	_, err := NewPublisher(nil)
	require.Error(t, err)
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)

	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_notify($1, $2)")).
		WithArgs(eventpkg.SignalMeshOptimizationProgress, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	progress := &apiv1.MeshOptimizationProgressEvent{JobId: "job", TimestampMs: 1}
	require.NoError(t, publisher.PublishMeshOptimizationProgress(context.Background(), progress))
	require.Equal(t, int64(1), progress.TimestampMs)

	for _, publish := range []func(context.Context) error{
		func(ctx context.Context) error {
			return publisher.PublishMeshOptimizationComplete(ctx, &apiv1.MeshOptimizationCompleteEvent{JobId: "complete"})
		},
		func(ctx context.Context) error {
			return publisher.PublishMeshOptimizationFail(ctx, &apiv1.MeshOptimizationFailEvent{JobId: "fail"})
		},
	} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
			WithArgs(eventpkg.QueueMeshOptimizationResult, sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
			WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
		require.NoError(t, publish(context.Background()))
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
		WithArgs(eventpkg.QueueMeshOptimizationResult, sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
		WillReturnError(errors.New("send failed"))
	require.ErrorContains(t, publisher.PublishMeshOptimizationComplete(context.Background(), &apiv1.MeshOptimizationCompleteEvent{JobId: "enqueue-failure"}), "pgmq send")

	require.Error(t, publisher.PublishMeshOptimizationProgress(context.Background(), &apiv1.MeshOptimizationProgressEvent{JobId: string([]byte{0xff})}))
	require.Error(t, publisher.PublishMeshOptimizationFail(context.Background(), &apiv1.MeshOptimizationFailEvent{JobId: string([]byte{0xff})}))
	require.Error(t, publisher.PublishMeshOptimizationProgress(context.Background(), &apiv1.MeshOptimizationProgressEvent{}))
	require.Error(t, publisher.PublishMeshOptimizationComplete(context.Background(), &apiv1.MeshOptimizationCompleteEvent{}))
	require.ErrorContains(t, publisher.PublishMeshOptimizationProgress(context.Background(), &apiv1.MeshOptimizationProgressEvent{JobId: "large", CorrelationId: strings.Repeat("x", 8_000)}), "payload limit")

	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_notify($1, $2)")).
		WithArgs(eventpkg.SignalMeshOptimizationProgress, sqlmock.AnyArg()).WillReturnError(errors.New("notify"))
	require.Error(t, publisher.PublishMeshOptimizationProgress(context.Background(), &apiv1.MeshOptimizationProgressEvent{JobId: "job"}))
	require.NoError(t, publisher.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPGMQHeaderCarrierAndCorrelationInjection(t *testing.T) {
	const requestID = "018f47a2-8a3d-4e17-9d42-6f12c89b1234"
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		requestID,
		sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceAssetOptimizer},
	)
	require.NoError(t, err)

	headers := injectMessageCorrelation(sharedtelemetry.WithRequestContext(context.Background(), requestContext), nil)
	carrier := pgmqHeaderCarrier(headers)
	require.Equal(t, requestID, carrier.Get(sharedtelemetry.MessageRequestIDHeader))
	require.ElementsMatch(t, []string{sharedtelemetry.MessageRequestIDHeader}, carrier.Keys())
}
