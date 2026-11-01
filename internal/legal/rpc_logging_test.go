package legal

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The handler leaves logging to the request record and keeps the database
// cause in the returned error.
func TestTermsRPCKeepsDatabaseCauseForTheRequestRecord(t *testing.T) {
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
	mutations, err := mutation.NewRunner(db, &auth.SpiceDBClient{}, identitystate.PostgresFence{}, &audittest.Recorder{})
	require.NoError(t, err)
	service := NewTermsConnectHandler(&Service{db: db, mutations: mutations})
	_, err = service.GetTermsVersion(t.Context(), connect.NewRequest(&managev1.GetTermsVersionRequest{Id: "terms-1"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Empty(t, output.String())
	cause := failurecause.Message(err)
	require.Contains(t, cause, `store failed: column "title" <policy> {constraint detail}`)
	require.NotContains(t, cause, "reader@example.test")
	require.NotContains(t, cause, "private-password")
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}
