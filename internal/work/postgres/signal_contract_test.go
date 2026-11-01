package workpostgres

import (
	"context"
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

type failingWorkSignalPublisher struct{ err error }

func (failingWorkSignalPublisher) EnqueueProtobuf(context.Context, string, string, proto.Message) error {
	return nil
}

func (failingWorkSignalPublisher) NotifyProtobuf(context.Context, string, proto.Message) error {
	return nil
}

func (p failingWorkSignalPublisher) NotifyProtobufWithExecutor(context.Context, eventpkg.DBTX, string, proto.Message) error {
	return p.err
}

func mockWorkSignalDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	require.NoError(t, err)
	return db, mock
}

func TestWorkContentUpdatedEventsSatisfySignalContract(t *testing.T) {
	targetRevision := "target-revision"
	events := map[string]*managev1.ContentUpdatedEvent{
		"manage update": buildManageWorkContentUpdatedEvent("work-1", []string{"featured"}),
		"lifecycle": buildManageStateTransitionContentUpdatedEvent(
			"work-1", []string{"state.status"},
		),
		"featured image": buildManageMediaMutationContentUpdatedEvent(
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, "work-1", "media.featured_image",
		),
		"collab source": buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:             "work-1",
			Fields:               []string{"content"},
			DocumentRevision:     "revision",
			Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			Locale:               "en",
			DocumentStateChanged: true,
		}),
		"collab target": buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:         "work-1",
			Fields:           []string{"title"},
			DocumentRevision: "revision",
			Source:           managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			Locale:           "ko",
			TargetRevision:   &targetRevision,
		}),
		"version restore": buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:             "work-1",
			Fields:               []string{"title", "summary", "content"},
			DocumentRevision:     "revision",
			Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
			Locale:               "en",
			DocumentStateChanged: true,
		}),
	}
	for name, event := range events {
		require.NotNil(t, event, name)
		if event.DocumentRevision == nil {
			require.ErrorContains(t, contentsignal.Validate(event), "document_revision", name)
			event.DocumentRevision = proto.String("revision") // populated under lock by PublishInTransaction
		}
		require.NoError(t, contentsignal.Validate(event), name)
	}
}

func TestPublishWorkContentUpdatedRequiresOwningTransaction(t *testing.T) {
	db, mock := mockWorkSignalDB(t)
	event := buildManageStateTransitionContentUpdatedEvent(
		"work-1", []string{"state.status"},
	)

	require.Error(t, contentsignal.PublishInTransaction(context.Background(), db, &mq.Publisher{}, event))

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT document.revision FROM work AS owner").WithArgs("work-1", 1).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow("revision"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::integer)")).WithArgs("content.updated", sqlmock.AnyArg(), sqlmock.AnyArg(), 0).WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_notify($1, $2)")).
		WithArgs(eventpkg.SignalContentUpdated, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return contentsignal.PublishInTransaction(context.Background(), tx, &mq.Publisher{}, event)
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublishWorkContentUpdatedFailureRollsBackMutation(t *testing.T) {
	db, mock := mockWorkSignalDB(t)
	failure := errors.New("notify failed")
	event := buildManageStateTransitionContentUpdatedEvent(
		"work-1", []string{"state.status"},
	)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT document.revision FROM work AS owner").WithArgs("work-1", 1).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow("revision"))
	mock.ExpectRollback()
	err := db.Transaction(func(tx *gorm.DB) error {
		return contentsignal.PublishInTransaction(context.Background(), tx, failingWorkSignalPublisher{err: failure}, event)
	})
	require.ErrorIs(t, err, failure)
	require.NoError(t, mock.ExpectationsWereMet())
}
