//go:build integration

package mutation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBestEffortReadRestoresFailedLookupAndKeepsOuterTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.SetupOryStack(t).DB
	require.NoError(t, db.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec("CREATE TEMP TABLE optional_read_probe (value integer NOT NULL) ON COMMIT DROP").Error)
		_, err := BestEffortRead(t.Context(), tx, func(read *gorm.DB) (int, error) {
			if err := read.Exec("INSERT INTO optional_read_probe VALUES (1)").Error; err != nil {
				return 0, err
			}
			return 0, read.Exec("SELECT 1 / 0").Error
		})
		require.ErrorContains(t, err, "division by zero")
		require.NoError(t, tx.Exec("INSERT INTO optional_read_probe VALUES (2)").Error)
		value, err := BestEffortRead(t.Context(), tx, func(read *gorm.DB) (int, error) {
			require.NoError(t, read.Exec("INSERT INTO optional_read_probe VALUES (3)").Error)
			_, nestedErr := BestEffortRead(t.Context(), read, func(nested *gorm.DB) (int, error) {
				require.NoError(t, nested.Exec("INSERT INTO optional_read_probe VALUES (4)").Error)
				return 0, nested.Exec("SELECT 1 / 0").Error
			})
			require.ErrorContains(t, nestedErr, "division by zero")
			var count int
			err := read.Raw("SELECT count(*) FROM optional_read_probe").Scan(&count).Error
			return count, err
		})
		require.NoError(t, err)
		require.Equal(t, 2, value)
		var values []int
		require.NoError(t, tx.Raw("SELECT value FROM optional_read_probe ORDER BY value").Scan(&values).Error)
		require.Equal(t, []int{2, 3}, values)
		return nil
	}))
}
