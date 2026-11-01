package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/series"
)

func TestSlugErrorKeepsTheDriverErrorReadable(t *testing.T) {
	violation := &pgconn.PgError{Code: "23505", ConstraintName: "series_slug_key", Message: "duplicate key value violates unique constraint"}

	err := slugError(violation)

	require.ErrorIs(t, err, series.ErrSlugTaken)
	var driver *pgconn.PgError
	require.ErrorAs(t, err, &driver)
	require.Equal(t, violation.Error(), err.Error())
	other := errors.New("connection reset")
	require.Same(t, other, slugError(other))
	require.NoError(t, slugError(nil))
}
