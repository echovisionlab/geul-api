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
	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobs"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func mockConnection(t *testing.T) (*Connection, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &Connection{db: db, dsn: "postgres://test"}, mock
}

func queueConfig() jobs.QueueConfig {
	return jobs.QueueConfig{Name: eventpkg.QueueTranscoderAudio, MessageType: "test.Command", Workers: 1, Timeout: time.Second, RetryLimit: 1}
}

func validMessage(t *testing.T, readCount int) eventpkg.Message {
	t.Helper()
	envelope, err := eventpkg.NewEnvelope("command-1", "test.Command", []byte("payload"))
	require.NoError(t, err)
	return eventpkg.Message{TransportID: 42, ReadCount: readCount, Envelope: envelope}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expectBoolean(mock sqlmock.Sqlmock, operation string, result bool) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq."+operation+"($1, $2::bigint)")).
		WithArgs(eventpkg.QueueTranscoderAudio, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"result"}).AddRow(result))
}

func expectRetry(mock sqlmock.Sqlmock, seconds int, err error) {
	expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT msg_id FROM pgmq.set_vt($1, $2::bigint, $3::integer)")).
		WithArgs(eventpkg.QueueTranscoderAudio, int64(42), seconds)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(42))
}

type fakeSignalConnection struct {
	execErr       error
	waitErr       error
	notifications []*pgconn.Notification
	closed        bool
}

func (f *fakeSignalConnection) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, f.execErr
}

func (f *fakeSignalConnection) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	if len(f.notifications) > 0 {
		notification := f.notifications[0]
		f.notifications = f.notifications[1:]
		return notification, nil
	}
	if f.waitErr != nil {
		return nil, f.waitErr
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSignalConnection) Close(context.Context) error {
	f.closed = true
	return nil
}

func preserveConnectionFactories(t *testing.T) {
	t.Helper()
	originalOpen := openPostgres
	originalSignal := connectSignal
	t.Cleanup(func() { openPostgres, connectSignal = originalOpen, originalSignal })
}

func TestConnectionValidation(t *testing.T) {
	preserveConnectionFactories(t)
	require.ErrorContains(t, func() error { _, err := NewConnection(""); return err }(), "DSN")
	_, err := NewConnection("://invalid")
	require.Error(t, err)
	_, err = connectSignal(context.Background(), "://invalid")
	require.Error(t, err)

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
}

func TestConnectionLifecycle(t *testing.T) {
	preserveConnectionFactories(t)
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
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
	require.NoError(t, connection.Close())
	require.NoError(t, (*Connection)(nil).Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConnectionSignals(t *testing.T) {
	preserveConnectionFactories(t)
	connectSignal = func(context.Context, string) (signalConnection, error) { return nil, errors.New("connect") }
	require.ErrorContains(t, (&Connection{dsn: "test"}).listen(context.Background(), "signal", func() {}, func([]byte) {}), "connect signal")
	fake := &fakeSignalConnection{execErr: errors.New("listen")}
	connectSignal = func(context.Context, string) (signalConnection, error) { return fake, nil }
	require.ErrorContains(t, (&Connection{dsn: "test"}).listen(context.Background(), "signal", func() {}, func([]byte) {}), "listen signal")
	require.True(t, fake.closed)
	fake = &fakeSignalConnection{notifications: []*pgconn.Notification{{Payload: "payload"}}, waitErr: errors.New("ended")}
	connectSignal = func(context.Context, string) (signalConnection, error) { return fake, nil }
	var payload string
	ready := false
	err := (&Connection{dsn: "test"}).listen(context.Background(), `signal"name`, func() { ready = true }, func(value []byte) { payload = string(value) })
	require.True(t, ready)
	require.Equal(t, "payload", payload)
	require.ErrorContains(t, err, "ended")
}

func TestConsumerValidationAndReadiness(t *testing.T) {
	config := queueConfig()
	_, err := NewConsumer(nil, config, func(context.Context, []byte) error { return nil })
	require.Error(t, err)
	connection, mock := mockConnection(t)
	connection.closed.Store(true)
	_, err = NewConsumer(connection, config, func(context.Context, []byte) error { return nil })
	require.Error(t, err)
	connection.closed.Store(false)
	_, err = NewConsumer(connection, jobs.QueueConfig{}, func(context.Context, []byte) error { return nil })
	require.Error(t, err)
	_, err = NewConsumer(connection, config, nil)
	require.ErrorContains(t, err, "handler")
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

type consumerProcessCase struct {
	name      string
	readCount int
	parent    func() context.Context
	handler   error
	expect    func(sqlmock.Sqlmock)
}

func consumerProcessCases() []consumerProcessCase {
	return []consumerProcessCase{
		{name: "success", readCount: 1, parent: context.Background, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", true) }},
		{name: "completion failure", readCount: 1, parent: context.Background, expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", false) }},
		{name: "cancelled", readCount: 1, parent: cancelledContext, handler: errors.New("cancelled"), expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 0, nil) }},
		{name: "retry", readCount: 1, parent: context.Background, handler: jobresult.Retry(errors.New("retry")), expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 5, nil) }},
		{name: "retry failure", readCount: 1, parent: context.Background, handler: jobresult.Retry(errors.New("retry")), expect: func(mock sqlmock.Sqlmock) { expectRetry(mock, 5, errors.New("retry")) }},
		{name: "terminal result", readCount: 1, parent: context.Background, handler: jobresult.Terminal(errors.New("terminal")), expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "delete", true) }},
		{name: "archive", readCount: 2, parent: context.Background, handler: errors.New("terminal"), expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "archive", true) }},
		{name: "archive failure", readCount: 2, parent: context.Background, handler: errors.New("terminal"), expect: func(mock sqlmock.Sqlmock) { expectBoolean(mock, "archive", false) }},
	}
}

func TestConsumerProcessingResults(t *testing.T) {
	config := queueConfig()
	for _, test := range consumerProcessCases() {
		t.Run(test.name, func(t *testing.T) {
			connection, mock := mockConnection(t)
			consumer, err := NewConsumer(connection, config, func(context.Context, []byte) error { return test.handler })
			require.NoError(t, err)
			test.expect(mock)
			consumer.process(test.parent(), validMessage(t, test.readCount))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestConsumerRejectsInvalidMessages(t *testing.T) {
	config := queueConfig()
	connection, mock := mockConnection(t)
	consumer, err := NewConsumer(connection, config, func(context.Context, []byte) error { return nil })
	require.NoError(t, err)
	expectBoolean(mock, "archive", true)
	consumer.process(context.Background(), eventpkg.Message{TransportID: 42, Envelope: eventpkg.Envelope{}})
	require.NoError(t, mock.ExpectationsWereMet())

	connection, mock = mockConnection(t)
	consumer, err = NewConsumer(connection, config, func(context.Context, []byte) error {
		t.Fatal("handler called")
		return nil
	})
	require.NoError(t, err)
	mismatched := validMessage(t, 1)
	mismatched.Envelope.MessageType = "other.Command"
	expectBoolean(mock, "archive", true)
	consumer.process(context.Background(), mismatched)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConsumerWorkerReadPaths(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Consumer, sqlmock.Sqlmock, context.Context)
	}{
		{name: "cancel during read", run: func(_ *Consumer, mock sqlmock.Sqlmock, _ context.Context) {
			mock.ExpectQuery("FROM pgmq.read").WillDelayFor(100 * time.Millisecond).WillReturnError(errors.New("read"))
		}},
		{name: "read error", run: func(_ *Consumer, mock sqlmock.Sqlmock, _ context.Context) {
			mock.ExpectQuery("FROM pgmq.read").WillReturnError(errors.New("read"))
		}},
		{name: "empty", run: func(_ *Consumer, mock sqlmock.Sqlmock, _ context.Context) {
			mock.ExpectQuery("FROM pgmq.read").WillReturnRows(sqlmock.NewRows([]string{"msg_id", "read_ct", "enqueued_at", "vt", "message", "headers"}))
		}},
		{name: "message", run: func(_ *Consumer, mock sqlmock.Sqlmock, _ context.Context) {
			message := validMessage(t, 1)
			envelopeJSON, err := json.Marshal(message.Envelope)
			require.NoError(t, err)
			now := time.Now()
			mock.ExpectQuery("FROM pgmq.read").WillReturnRows(sqlmock.NewRows([]string{"msg_id", "read_ct", "enqueued_at", "vt", "message", "headers"}).AddRow(42, 1, now, now, envelopeJSON, []byte("{}")))
			expectBoolean(mock, "delete", true)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection, mock := mockConnection(t)
			consumer, err := NewConsumer(connection, queueConfig(), func(context.Context, []byte) error { return nil })
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			test.run(consumer, mock, ctx)
			consumer.wg.Add(1)
			done := make(chan struct{})
			go func() { consumer.worker(ctx, 0); close(done) }()
			time.Sleep(20 * time.Millisecond)
			if test.name == "empty" {
				close(consumer.done)
				<-done
				cancel()
			} else {
				cancel()
				<-done
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func expectEnqueue(mock sqlmock.Sqlmock, queue string, result int64, err error) {
	expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
		WithArgs(queue, sqlmock.AnyArg(), sqlmock.AnyArg(), 0)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(result))
}

func mockPublisher(t *testing.T) (*Publisher, sqlmock.Sqlmock) {
	t.Helper()
	connection, mock := mockConnection(t)
	publisher, err := NewPublisher(connection)
	require.NoError(t, err)
	return publisher, mock
}

func TestPublisherValidation(t *testing.T) {
	_, err := NewPublisher(nil)
	require.Error(t, err)
}

func TestPublisherResults(t *testing.T) {
	publisher, mock := mockPublisher(t)
	expectEnqueue(mock, eventpkg.QueueTranscodeResult, 1, nil)
	complete := &apiv1.TranscodeCompleteEvent{EventId: "transcode", TimestampMs: 1}
	require.NoError(t, publisher.PublishComplete(context.Background(), complete))
	require.Equal(t, int64(1), complete.TimestampMs)
	for _, value := range []struct {
		queue   string
		publish func(context.Context) error
	}{
		{eventpkg.QueueWaveformResult, func(ctx context.Context) error {
			return publisher.PublishWaveformComplete(ctx, &apiv1.WaveformCompleteEvent{EventId: "wave-complete"})
		}},
		{eventpkg.QueueWaveformResult, func(ctx context.Context) error {
			return publisher.PublishWaveformFail(ctx, &apiv1.WaveformFailEvent{EventId: "wave-fail"})
		}},
	} {
		expectEnqueue(mock, value.queue, 1, nil)
		require.NoError(t, value.publish(context.Background()))
	}
	require.NoError(t, publisher.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublisherProgress(t *testing.T) {
	publisher, mock := mockPublisher(t)
	for _, value := range []struct {
		signal  string
		publish func(context.Context) error
	}{
		{eventpkg.SignalTranscodeProgress, func(ctx context.Context) error {
			return publisher.PublishProgress(ctx, &apiv1.TranscodeProgressEvent{EventId: "progress"})
		}},
		{eventpkg.SignalWaveformProgress, func(ctx context.Context) error {
			return publisher.PublishWaveformProgress(ctx, &apiv1.WaveformProgressEvent{EventId: "wave-progress"})
		}},
	} {
		mock.ExpectExec(regexp.QuoteMeta("SELECT pg_notify($1, $2)")).WithArgs(value.signal, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, value.publish(context.Background()))
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func requiredProtoMessage(t *testing.T) proto.Message {
	t.Helper()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto2"),
		Name:    proto.String("required.proto"),
		Package: proto.String("mqtest"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("RequiredMessage"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:   proto.String("required_value"),
				Number: proto.Int32(1),
				Label:  descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum(),
				Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}, nil)
	require.NoError(t, err)
	return dynamicpb.NewMessage(file.Messages().Get(0))
}

func TestPublisherResultFailures(t *testing.T) {
	publisher, mock := mockPublisher(t)
	expectEnqueue(mock, eventpkg.QueueTranscodeResult, 0, errors.New("send"))
	require.ErrorContains(t, publisher.PublishComplete(context.Background(), &apiv1.TranscodeCompleteEvent{EventId: "failed"}), "publish transcode result")
	require.ErrorContains(t, publisher.enqueue(context.Background(), eventpkg.QueueTranscodeResult, "required", "mqtest.RequiredMessage", requiredProtoMessage(t)), "marshal event")
	require.Error(t, publisher.enqueue(context.Background(), eventpkg.QueueTranscodeResult, "", "", &apiv1.TranscodeCompleteEvent{}))
	require.Error(t, publisher.PublishComplete(context.Background(), &apiv1.TranscodeCompleteEvent{EventId: string([]byte{0xff})}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublisherProgressFailures(t *testing.T) {
	publisher, mock := mockPublisher(t)
	require.Error(t, publisher.PublishProgress(context.Background(), &apiv1.TranscodeProgressEvent{EventId: string([]byte{0xff})}))
	require.Error(t, publisher.PublishProgress(context.Background(), &apiv1.TranscodeProgressEvent{}))
	require.ErrorContains(t, publisher.PublishProgress(context.Background(), &apiv1.TranscodeProgressEvent{EventId: "large", EntityId: strings.Repeat("x", 8_000)}), "payload limit")
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_notify($1, $2)")).WithArgs(eventpkg.SignalTranscodeProgress, sqlmock.AnyArg()).WillReturnError(errors.New("notify"))
	require.Error(t, publisher.PublishProgress(context.Background(), &apiv1.TranscodeProgressEvent{EventId: "notify"}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func cancelEnvelope(t *testing.T, value proto.Message, messageID, messageType string) []byte {
	t.Helper()
	body, err := proto.Marshal(value)
	require.NoError(t, err)
	envelope, err := eventpkg.NewEnvelope(messageID, messageType, body)
	require.NoError(t, err)
	payload, err := json.Marshal(envelope)
	require.NoError(t, err)
	return payload
}

func TestCancelSubscriberValidation(t *testing.T) {
	_, err := newCancelSubscriber(nil, "signal", func([]byte) (string, error) { return "", nil }, func(string) bool { return true })
	require.Error(t, err)
	_, err = newCancelSubscriber(&Connection{}, "signal", nil, func(string) bool { return true })
	require.Error(t, err)
	_, err = newCancelSubscriber(&Connection{}, "signal", func([]byte) (string, error) { return "", nil }, nil)
	require.Error(t, err)
	_, err = NewCancelSubscriber(nil, func(string) bool { return true })
	require.Error(t, err)
	_, err = NewWaveformCancelSubscriber(nil, func(string) bool { return true })
	require.Error(t, err)
	require.NoError(t, (*cancelSubscriber)(nil).Close())
}

func TestCancelSubscriberNotification(t *testing.T) {
	preserveConnectionFactories(t)
	received := make(chan string, 1)
	fake := &fakeSignalConnection{
		notifications: []*pgconn.Notification{{Payload: string(cancelEnvelope(t, &apiv1.TranscodeCancelEvent{FileId: "notified-file"}, "cancel", "api.manage.v1.TranscodeCancelEvent"))}},
		waitErr:       errors.New("listener ended"),
	}
	connectSignal = func(context.Context, string) (signalConnection, error) { return fake, nil }
	notified, err := NewCancelSubscriber(&Connection{dsn: "test"}, func(id string) bool {
		received <- id
		return true
	})
	require.NoError(t, err)
	require.Equal(t, "notified-file", <-received)
	require.NoError(t, notified.Close())
	require.True(t, fake.closed)
}

func TestCancelSubscriberStartupFailures(t *testing.T) {
	preserveConnectionFactories(t)
	started := make(chan struct{})
	connectSignal = func(context.Context, string) (signalConnection, error) {
		close(started)
		return nil, errors.New("offline")
	}
	transcode, err := NewCancelSubscriber(&Connection{dsn: "test"}, func(string) bool { return true })
	require.ErrorContains(t, err, "offline")
	<-started
	require.Nil(t, transcode)
	connectSignal = func(context.Context, string) (signalConnection, error) { return nil, errors.New("offline") }
	waveform, err := NewWaveformCancelSubscriber(&Connection{dsn: "test"}, func(string) bool { return true })
	require.ErrorContains(t, err, "offline")
	require.Nil(t, waveform)
}

func TestHandleCancelSignal(t *testing.T) {
	var got string
	handleCancelSignal(cancelEnvelope(t, &apiv1.TranscodeCancelEvent{FileId: "file"}, "cancel", "api.manage.v1.TranscodeCancelEvent"), decodeTranscodeCancel, func(id string) bool { got = id; return true })
	require.Equal(t, "file", got)
	handleCancelSignal(cancelEnvelope(t, &apiv1.WaveformCancelEvent{EventId: "event"}, "wave", "api.manage.v1.WaveformCancelEvent"), decodeWaveformCancel, func(id string) bool { got = id; return true })
	require.Equal(t, "event", got)
	handleCancelSignal([]byte("invalid"), decodeTranscodeCancel, func(string) bool { t.Fatal("called"); return true })
	invalidPayload, err := json.Marshal(eventpkg.Envelope{MessageID: "bad", MessageType: "bad", SchemaVersion: 1, CreatedAt: time.Now().Format(time.RFC3339Nano), PayloadBase64: "%%%"})
	require.NoError(t, err)
	handleCancelSignal(invalidPayload, decodeTranscodeCancel, func(string) bool { t.Fatal("called"); return true })
	badDecode := cancelEnvelope(t, &apiv1.TranscodeCancelEvent{}, "bad", "api.manage.v1.TranscodeCancelEvent")
	handleCancelSignal(badDecode, func([]byte) (string, error) { return "", errors.New("decode") }, func(string) bool { t.Fatal("called"); return true })
}

func TestCancelDecoders(t *testing.T) {
	require.Error(t, func() error { _, err := decodeTranscodeCancel([]byte("bad")); return err }())
	require.Error(t, func() error { _, err := decodeWaveformCancel([]byte("bad")); return err }())
}
