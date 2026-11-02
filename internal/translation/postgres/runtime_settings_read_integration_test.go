//go:build integration

package translationpostgres_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestRuntimeSettingsCanReadSnapshotWithoutWritingIntegration(t *testing.T) {
	t.Parallel()
	for _, initialized := range []bool{false, true} {
		name := "missing"
		if initialized {
			name = "stored"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
			require.NoError(t, db.Exec("DELETE FROM translation_settings").Error)
			stamp := time.Now().UTC().Truncate(time.Microsecond)
			if initialized {
				require.NoError(t, db.Exec(`INSERT INTO translation_settings (id, default_locale, protected_terms, updated_at) VALUES (1, 'ko', ARRAY['Geul','Rawsector'], ?)`, stamp).Error)
			}
			var settings translation.RuntimeSettings
			require.NoError(t, mutation.Read(t.Context(), db, func(tx *gorm.DB) error {
				var err error
				settings, err = translation.LoadRuntimeSettings(t.Context(), tx)
				if err != nil {
					return err
				}
				return tx.WithContext(t.Context()).Exec("SELECT 1").Error
			}))
			if initialized {
				require.Equal(t, "ko", settings.DefaultLocale)
				require.Equal(t, []string{"Geul", "Rawsector"}, settings.ProtectedTerms)
				require.NotNil(t, settings.UpdatedAt)
				require.True(t, stamp.Equal(*settings.UpdatedAt))
			} else {
				require.Equal(t, translation.DefaultRuntimeSettings(), settings)
			}
			var count int64
			require.NoError(t, db.WithContext(t.Context()).Table("translation_settings").Count(&count).Error)
			want := int64(0)
			if initialized {
				want = 1
			}
			require.Equal(t, want, count)
		})
	}
}
