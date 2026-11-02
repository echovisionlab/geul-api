//go:build integration

package publiccontent_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
)

func TestPolicySQLFailureKeepsLocalizationSnapshotUsableIntegration(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name            string
		batch, snapshot bool
	}{
		{"single pool", false, false}, {"single snapshot", false, true},
		{"batch pool", true, false}, {"batch snapshot", true, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
			documentID, id := uuid.NewString(), uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?, 'compact', ?)`, documentID, uuid.NewString()).Error)
			require.NoError(t, db.Exec(`INSERT INTO series (id, slug, source_locale, content_document_id) VALUES (?, ?, 'en', ?)`, id, "policy-fallback-"+id, documentID).Error)
			require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title) VALUES (?, 'en', 'Source'), (?, 'ko', '번역')`, id, id).Error)
			require.NoError(t, db.Exec("ALTER TABLE translation_settings RENAME TO unavailable_translation_settings").Error)
			spec := publiccontent.Spec{EntityType: "series", TableName: "series_translation", SelectClause: "locale, title"}
			var selection publiccontent.Selection
			read := func(tx *gorm.DB) error {
				reader := translationpostgres.NewRuntimeSettingsReader(tx)
				if mode.batch {
					rows, err := publiccontent.ResolveBatch(t.Context(), tx, spec, []string{id}, "ko", reader)
					if err != nil {
						return err
					}
					selection = rows[id]
				} else {
					var err error
					selection, err = publiccontent.Resolve(t.Context(), tx, spec, id, "ko", reader)
					if err != nil {
						return err
					}
				}
				var probe int
				if err := tx.Raw("SELECT 42").Scan(&probe).Error; err != nil {
					return err
				}
				require.Equal(t, 42, probe)
				return nil
			}
			if mode.snapshot {
				require.NoError(t, mutation.Read(t.Context(), db, read))
			} else {
				require.NoError(t, read(db))
			}
			require.Equal(t, "en", selection.SourceLocale)
			require.Equal(t, "ko", selection.DisplayedLocale)
			require.NotNil(t, selection.Title)
			require.Equal(t, "번역", *selection.Title)
		})
	}
}
