package dberrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestIsSerializationFailureMatchesOnlySQLState40001(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "postgres serialization failure", err: &pgconn.PgError{Code: "40001"}, want: true},
		{name: "wrapped postgres serialization failure", err: fmt.Errorf("save document: %w", &pgconn.PgError{Code: "40001"}), want: true},
		{name: "wrapped driver SQLSTATE", err: fmt.Errorf("save document: %w", driverStateError{state: "40001"}), want: true},
		{name: "deadlock is not serialization failure", err: &pgconn.PgError{Code: "40P01"}, want: false},
		{name: "other SQLSTATE", err: driverStateError{state: "23505"}, want: false},
		{name: "text mentioning SQLSTATE is not typed", err: errors.New("database returned SQLSTATE 40001"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, IsSerializationFailure(test.err))
		})
	}
}

type driverStateError struct {
	state string
}

func (err driverStateError) Error() string    { return "database error" }
func (err driverStateError) SQLState() string { return err.state }
