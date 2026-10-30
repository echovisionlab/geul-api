package dberrors

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// transientSQLStateClasses are PostgreSQL SQLSTATE classes that describe the
// server or connection rather than the statement: connection exceptions,
// transaction rollbacks, insufficient resources, operator intervention and
// system errors.
var transientSQLStateClasses = map[string]bool{
	"08": true, "40": true, "53": true, "57": true, "58": true,
}

// IsTransient reports whether err comes from an unavailable or interrupted
// database rather than from the statement or its data, so the same work can
// succeed when retried later.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return true
	}
	var stateError interface {
		error
		SQLState() string
	}
	if errors.As(err, &stateError) {
		state := stateError.SQLState()
		return len(state) == 5 && transientSQLStateClasses[state[:2]]
	}
	return false
}
