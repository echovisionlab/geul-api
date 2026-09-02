package maptheme

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestMapThemeDefaultDeleteViolationRecognizesPostgresConstraintCodes(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"23001", "23503"} {
		err := fmt.Errorf("delete map theme: %w", &pgconn.PgError{
			Code:           code,
			ConstraintName: "site_settings_default_map_theme_id_fkey",
		})
		require.True(t, isMapThemeDefaultDeleteViolation(err), code)
	}
	require.False(t, isMapThemeDefaultDeleteViolation(&pgconn.PgError{
		Code:           "23503",
		ConstraintName: "different_constraint",
	}))
}
