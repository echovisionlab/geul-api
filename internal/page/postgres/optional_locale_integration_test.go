//go:build integration

package pagepostgres_test

import (
	"context"
	"testing"

	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failingPreferredLocale struct{ pagepostgres.Members }

func (failingPreferredLocale) PreferredLocale(ctx context.Context, tx *gorm.DB, _ string) (*string, error) {
	return nil, tx.WithContext(ctx).Exec("SELECT 1 / 0").Error
}
func TestOptionalLocaleLookupFailureKeepsTheCreationTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	require.NoError(t, db.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		store := pagepostgres.NewStore(tx, pagepostgres.Ports{Members: failingPreferredLocale{}})
		_, err := store.PreferredLocale(t.Context(), uuid.NewString())
		require.ErrorContains(t, err, "division by zero")
		require.NoError(t, tx.Exec("ALTER TABLE translation_settings RENAME COLUMN default_locale TO optional_read_unavailable").Error)
		_, err = store.RuntimeSettings(t.Context())
		var queryError *pgconn.PgError
		require.ErrorAs(t, err, &queryError)
		require.Equal(t, "42703", queryError.Code)
		require.NoError(t, tx.Exec("ALTER TABLE translation_settings RENAME COLUMN optional_read_unavailable TO default_locale").Error)
		settings, err := store.RuntimeSettings(t.Context())
		require.NoError(t, err)
		require.Equal(t, "en", settings.DefaultLocale)
		var value int
		require.NoError(t, tx.Raw("SELECT 42").Scan(&value).Error)
		require.Equal(t, 42, value)
		return nil
	}))
}
