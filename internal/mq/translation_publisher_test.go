package mq

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestTranslationEnqueueUsesCallerTransactionWithoutPublisherPool(t *testing.T) {
	t.Parallel()
	for _, enqueueFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "enqueue failure rolls back"}[enqueueFailure], func(t *testing.T) {
			t.Parallel()
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
			require.NoError(t, err)
			mock.ExpectBegin()
			query := mock.ExpectQuery(`SELECT pgmq\.send\(`)
			cause := errors.New("queue unavailable")
			if enqueueFailure {
				query.WillReturnError(cause)
				mock.ExpectRollback()
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"msg_id"}).AddRow(1))
				mock.ExpectCommit()
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				return (&Publisher{}).EnqueueTranslationGenerateWithDB(t.Context(), tx, &managev1.TranslationGenerateEvent{JobId: "translation-job"})
			})
			if enqueueFailure {
				require.ErrorIs(t, err, cause)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTranslationEnqueueRejectsMissingJobAndNontransactionalDatabase(t *testing.T) {
	t.Parallel()
	publisher := &Publisher{}
	require.ErrorContains(t, publisher.EnqueueTranslationGenerateWithDB(t.Context(), nil, nil), "job is required")
	job := &managev1.TranslationGenerateEvent{JobId: "translation-job"}
	require.ErrorContains(t, publisher.EnqueueTranslationGenerateWithDB(t.Context(), nil, job), "transaction is required")
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	require.ErrorContains(t, publisher.EnqueueTranslationGenerateWithDB(t.Context(), db, job), "owning transaction")
	require.NoError(t, mock.ExpectationsWereMet())
}
