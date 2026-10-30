package dberrors

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIsTransientSeparatesOutagesFromStatementErrors(t *testing.T) {
	for name, err := range map[string]error{
		"deadline":           fmt.Errorf("load template: %w", context.DeadlineExceeded),
		"bad connection":     driver.ErrBadConn,
		"network":            &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		"admin shutdown":     &pgconn.PgError{Code: "57P01"},
		"cannot connect now": &pgconn.PgError{Code: "57P03"},
		"connection failure": &pgconn.PgError{Code: "08006"},
		"too many clients":   &pgconn.PgError{Code: "53300"},
		"serialization":      fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"}),
		"driver state 08":    sqlStateError("08001"),
	} {
		require.True(t, IsTransient(err), name)
	}
	for name, err := range map[string]error{
		"nil":              nil,
		"not found":        gorm.ErrRecordNotFound,
		"unique violation": &pgconn.PgError{Code: "23505"},
		"undefined column": &pgconn.PgError{Code: "42703"},
		"template error":   errors.New("render email subject: missing variable"),
		"malformed state":  sqlStateError("08"),
	} {
		require.False(t, IsTransient(err), name)
	}
}
