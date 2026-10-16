package legal

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTermsRPCLogsDatabaseCauseBeforeReturningStatus(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "terms_history"`).WillReturnError(errors.New(`store failed: column "title" <policy> {constraint detail} reader@example.test password=private-password`))
	mock.ExpectRollback()
	service := &TermsService{db: db}
	_, err = service.GetTermsVersion(t.Context(), connect.NewRequest(&managev1.GetTermsVersionRequest{Id: "terms-1"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "GetTermsVersion", entry["method"])
	require.Contains(t, entry["failure_cause"], `store failed: column "title" <policy> {constraint detail}`)
	require.NotContains(t, output.String(), "reader@example.test")
	require.NotContains(t, output.String(), "private-password")
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}
