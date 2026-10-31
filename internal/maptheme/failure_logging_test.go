package maptheme

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The handler does not log: the cause stays in the returned error for the one
// request.completed record.
func TestListMapThemesKeepsCauseForTheRequestRecord(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	cause := errors.New("transaction backend unavailable")
	mock.ExpectBegin().WillReturnError(cause)
	service := &MapThemeService{db: db}
	response, err := service.ListMapThemes(t.Context(), connect.NewRequest(&managev1.ListMapThemesRequest{}))
	require.Nil(t, response)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.ErrorIs(t, err, cause)
	require.Empty(t, output.String())
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}
