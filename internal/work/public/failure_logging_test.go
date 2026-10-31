package public

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOptionalRelationReadFailuresLogCauseAndPreserveFallback(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
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
	service := &Service{db: db}
	err = db.Transaction(func(tx *gorm.DB) error {
		snapshot, readErr := service.loadWorkPublicRelationsSnapshot(t.Context(), tx, &model.Work{ID: "work-1"})
		require.NoError(t, readErr)
		require.Empty(t, snapshot.CreditGroups)
		require.Empty(t, snapshot.Clients)
		return readErr
	})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 2)
	for i, cause := range []string{"credit groups unavailable", "clients unavailable"} {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &entry))
		require.Equal(t, "ERROR", entry["level"])
		require.Equal(t, cause, entry["failure_cause"])
		require.Equal(t, "work-1", entry["work_id"])
	}
}
