package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/maptheme"
)

func TestMapThemeDefaultDeleteViolationRecognizesPostgresConstraintCodes(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"23001", "23503"} {
		err := fmt.Errorf("delete map theme: %w", &pgconn.PgError{
			Code:           code,
			ConstraintName: "site_settings_default_map_theme_id_fkey",
		})
		require.True(t, isDefaultDeleteViolation(err), code)
	}
	require.False(t, isDefaultDeleteViolation(&pgconn.PgError{
		Code:           "23503",
		ConstraintName: "different_constraint",
	}))
}

func TestDefaultThemeErrorKeepsTheDriverErrorInTheChain(t *testing.T) {
	t.Parallel()
	violation := &pgconn.PgError{Code: "23001", ConstraintName: "site_settings_default_map_theme_id_fkey", Message: "restricted"}

	err := error(defaultThemeError{violation})

	require.ErrorIs(t, err, maptheme.ErrDefaultTheme)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, violation.Error(), err.Error())
}
