package workpublicpostgres

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOptionalRelationReadFailuresRollBackTheirSavepoint(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec("SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT .* FROM "work_credit_group"`).WillReturnError(errors.New("credit groups unavailable"))
	mock.ExpectExec("ROLLBACK TO SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT .* FROM "work_credit"`).WillReturnRows(sqlmock.NewRows([]string{"id", "work_id"}))
	mock.ExpectExec("SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT .* FROM work_client wc").WillReturnError(errors.New("clients unavailable"))
	mock.ExpectExec("ROLLBACK TO SAVEPOINT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectClose()
	err = db.Transaction(func(tx *gorm.DB) error {
		reads := store{tx: tx}
		groups, readErr := reads.CreditGroups(t.Context(), "work-1")
		require.ErrorContains(t, readErr, "credit groups unavailable")
		require.Empty(t, groups)
		credits, readErr := reads.Credits(t.Context(), "work-1")
		require.NoError(t, readErr)
		require.Empty(t, credits)
		clients, readErr := reads.Clients(t.Context(), "work-1")
		require.ErrorContains(t, readErr, "clients unavailable")
		require.Empty(t, clients)
		return nil
	})
	require.NoError(t, err)
}
