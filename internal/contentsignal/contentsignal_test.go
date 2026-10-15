package contentsignal_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mq"
)

var notifyQuery = regexp.QuoteMeta("SELECT pg_notify($1, $2)")

func mockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)
	return db, mock
}

func collabEvent() *managev1.ContentUpdatedEvent {
	revision := "0199a3c4-0000-7000-8000-000000000001"
	locale := "en"
	exists := true
	return &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		EntityId:             "0199a3c4-0000-7000-8000-0000000000aa",
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
		DocumentRevision:     &revision,
		DocumentStateChanged: true,
		Locale:               &locale,
		LocaleExists:         &exists,
	}
}

// matchesEnvelope decodes the NOTIFY payload and requires the exact event.
type matchesEnvelope struct {
	t     *testing.T
	event *managev1.ContentUpdatedEvent
}

func (m matchesEnvelope) Match(value driver.Value) bool {
	payload, ok := value.(string)
	if !ok {
		return false
	}
	var envelope eventpkg.Envelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return false
	}
	body, err := envelope.Payload()
	if err != nil {
		return false
	}
	decoded := &managev1.ContentUpdatedEvent{}
	if err := proto.Unmarshal(body, decoded); err != nil {
		return false
	}
	return proto.Equal(decoded, m.event)
}

func TestPublishInTransactionCommitSignalsExactlyOnce(t *testing.T) {
	t.Parallel()
	db, mock := mockDB(t)
	event := collabEvent()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "post"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
		WithArgs("content.updated", sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
	mock.ExpectExec(notifyQuery).
		WithArgs(eventpkg.SignalContentUpdated, matchesEnvelope{t: t, event: event}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE "post" SET updated_at = now()`).Error; err != nil {
			return err
		}
		return contentsignal.PublishInTransaction(context.Background(), tx, &mq.Publisher{}, event)
	})
	require.NoError(t, err)
	// sqlmock fails on any unexpected statement, so exactly one pg_notify ran,
	// and it ran before COMMIT on the transaction connection.
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublishInTransactionRollbackDiscardsSignal(t *testing.T) {
	t.Parallel()
	db, mock := mockDB(t)
	failure := errors.New("audit append failed")

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
		WithArgs("content.updated", sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
	mock.ExpectExec(notifyQuery).
		WithArgs(eventpkg.SignalContentUpdated, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := contentsignal.PublishInTransaction(context.Background(), tx, &mq.Publisher{}, collabEvent()); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	// The only NOTIFY was issued on the rolled-back transaction; PostgreSQL
	// never delivers it. No statement ran outside the transaction.
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublishInTransactionFailureRollsBackMutation(t *testing.T) {
	t.Parallel()
	db, mock := mockDB(t)
	notifyErr := errors.New("connection reset")

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "post"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).
		WithArgs("content.updated", sqlmock.AnyArg(), sqlmock.AnyArg(), 0).
		WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
	mock.ExpectExec(notifyQuery).WillReturnError(notifyErr)
	mock.ExpectRollback()

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE "post" SET updated_at = now()`).Error; err != nil {
			return err
		}
		return contentsignal.PublishInTransaction(context.Background(), tx, &mq.Publisher{}, collabEvent())
	})
	require.ErrorIs(t, err, notifyErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublishInTransactionRefusesNonTransactionalPool(t *testing.T) {
	t.Parallel()
	db, mock := mockDB(t)
	err := contentsignal.PublishInTransaction(context.Background(), db, &mq.Publisher{}, collabEvent())
	require.ErrorContains(t, err, "inside the owning database transaction")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublishInTransactionNilEventIsNoop(t *testing.T) {
	t.Parallel()
	require.NoError(t, contentsignal.PublishInTransaction(context.Background(), nil, nil, nil))
}

func TestPublishInTransactionRequiresPublisher(t *testing.T) {
	t.Parallel()
	err := contentsignal.PublishInTransaction(context.Background(), nil, nil, collabEvent())
	require.ErrorIs(t, err, contentsignal.ErrPublisherRequired)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	blank := " "
	target := "target-token"
	falseValue := false
	cases := map[string]struct {
		mutate  func(*managev1.ContentUpdatedEvent)
		wantErr string
	}{
		"valid source change": {mutate: func(*managev1.ContentUpdatedEvent) {}},
		"valid target change": {mutate: func(event *managev1.ContentUpdatedEvent) {
			event.DocumentStateChanged = false
			event.TargetRevision = &target
		}},
		"valid metadata-only": {mutate: func(event *managev1.ContentUpdatedEvent) {
			event.DocumentStateChanged = false
			event.Locale = nil
			event.LocaleExists = nil
		}},
		"missing source": {
			mutate: func(event *managev1.ContentUpdatedEvent) {
				event.Source = managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
			},
			wantErr: "source is required",
		},
		"missing revision": {
			mutate:  func(event *managev1.ContentUpdatedEvent) { event.DocumentRevision = nil },
			wantErr: "document_revision is required",
		},
		"blank revision": {
			mutate:  func(event *managev1.ContentUpdatedEvent) { event.DocumentRevision = &blank },
			wantErr: "must not be blank",
		},
		"locale without exists": {
			mutate:  func(event *managev1.ContentUpdatedEvent) { event.LocaleExists = nil },
			wantErr: "locale_exists is required",
		},
		"target on deleted locale": {
			mutate: func(event *managev1.ContentUpdatedEvent) {
				event.DocumentStateChanged = false
				event.LocaleExists = &falseValue
				event.TargetRevision = &target
			},
			wantErr: "existing target-only change",
		},
		"target masquerading as source": {
			mutate:  func(event *managev1.ContentUpdatedEvent) { event.TargetRevision = &target },
			wantErr: "existing target-only change",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			event := collabEvent()
			tc.mutate(event)
			err := contentsignal.Validate(event)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestPublishInTransactionEnqueueFailureRollsBackWithoutNotify(t *testing.T) {
	db, mock := mockDB(t)
	failure := errors.New("queue unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "post"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).WillReturnError(failure)
	mock.ExpectRollback()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE "post" SET updated_at = now()`).Error; err != nil {
			return err
		}
		return contentsignal.PublishInTransaction(context.Background(), tx, &mq.Publisher{}, collabEvent())
	})
	require.ErrorIs(t, err, failure)
	require.NoError(t, mock.ExpectationsWereMet())
}
