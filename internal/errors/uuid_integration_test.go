//go:build integration

package errors_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func TestMalformedRequestUUIDIsAClientErrorWithTheActualDriverIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{}).DB
	var value string
	err := db.Raw("SELECT ?::uuid", "private-invalid-request-id").Scan(&value).Error
	require.Error(t, err)
	var driverError *pgconn.PgError
	require.ErrorAs(t, err, &driverError)
	require.Equal(t, "22P02", driverError.Code)
	classified, ok := errs.Classified(err)
	require.True(t, ok, "MCP callers classify the native error without the Connect wrapper")
	require.Equal(t, connect.CodeInvalidArgument, classified.Code())
	answer := errs.Wrap(err)
	require.Equal(t, connect.CodeInvalidArgument, answer.Code())
	require.Equal(t, "request contains an invalid UUID", answer.Message())
	require.True(t, errors.Is(answer, err))
}

func TestStoredUUIDConversionFailureStaysInternalIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{}).DB
	require.NoError(t, db.Exec("CREATE TABLE uuid_value (value text NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO uuid_value (value) VALUES (?)", "private-invalid-stored-id").Error)
	var value string
	err := db.Raw("SELECT value::uuid FROM uuid_value").Scan(&value).Error
	require.Error(t, err)
	var driverError *pgconn.PgError
	require.ErrorAs(t, err, &driverError)
	require.Equal(t, "22P02", driverError.Code)
	answer := errs.Wrap(err)
	require.Equal(t, connect.CodeInternal, answer.Code())
	require.True(t, errors.Is(answer, err))
}
