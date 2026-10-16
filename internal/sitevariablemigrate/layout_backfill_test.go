package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/stretchr/testify/require"
)

func layoutRow(locale, html string) string {
	raw, _ := json.Marshal(map[string]string{"entity_id": "legacy", "locale": locale, "html_content": html, "content_text": "preserve original text"})
	return string(raw)
}
func expectLayoutInventory(mock sqlmock.Sqlmock, locales map[string]string) {
	keys := []string{}
	for locale := range locales {
		keys = append(keys, locale)
	}
	sort.Strings(keys)
	for _, spec := range tables {
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(spec.table == "email_layout_translation"))
		if spec.table != "email_layout_translation" {
			continue
		}
		rows := sqlmock.NewRows([]string{"row"})
		for _, locale := range keys {
			rows.AddRow(layoutRow(locale, locales[locale]))
		}
		mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "email_layout_translation"`).WillReturnRows(rows)
		mock.ExpectQuery(`SELECT source_locale, content_document_id FROM "email_layout"`).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"source_locale", "content_document_id"}).AddRow("en", nil))
	}
}
func expectLayoutValidation(mock sqlmock.Sqlmock, locales map[string]string, invalid bool) {
	expectEmailSettings(mock)
	for _, locale := range []string{"en", "ko"} {
		mock.ExpectQuery(`SELECT "html_content" FROM "email_layout_translation"`).WithArgs("legacy", locale, 1).WillReturnRows(sqlmock.NewRows([]string{"html_content"}).AddRow(locales[locale]))
		if invalid {
			return
		}
		mock.ExpectQuery(`SELECT root.source_locale, source.html_content FROM email_layout AS root`).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"source_locale", "html_content"}).AddRow("en", locales["en"]))
		mock.ExpectQuery(`SELECT source_locale AS locale FROM "email_layout"`).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"locale"}).AddRow("en"))
		mock.ExpectQuery(`SELECT "html_content" FROM "email_layout_translation"`).WithArgs("legacy", "en", 1).WillReturnRows(sqlmock.NewRows([]string{"html_content"}).AddRow(locales["en"]))
		if locale != "en" {
			mock.ExpectQuery(`SELECT locale, html_content FROM "email_layout_translation"`).WithArgs("legacy", "ko").WillReturnRows(sqlmock.NewRows([]string{"locale", "html_content"}).AddRow("ko", locales["ko"]))
		}
	}
}

func TestLayoutMarkerOnlyBackfillIsIncludedInDryRun(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectLayoutInventory(mock, map[string]string{"en": `<img alt="Logo">{{content}}<p>Help</p>`, "ko": `<img alt="로고">{{content}}<p>도움말</p>`})
	var report bytes.Buffer
	require.NoError(t, Run(t.Context(), db, false, &report))
	require.Contains(t, report.String(), "email_layout_translation.html_content locale=en changes=1")
	require.Contains(t, report.String(), "email_layout_translation.html_content locale=ko changes=1")
	require.Contains(t, report.String(), "owners=1 apply=false")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLayoutBackfillApplyBacksUpWholeOwnerValidatesAndIsIdempotent(t *testing.T) {
	t.Setenv("SITE_ORIGIN", "https://www.rawsector.com")
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint("invalid=", invalid), func(t *testing.T) {
			db, mock := migrationTestDB(t)
			source := `<img alt="Brand"><p>DSUB</p>{{content}}<a href="mailto:support@dsub.io">Help</a>`
			target := `<img alt="브랜드"><p>DSUB</p>{{content}}<a href="mailto:support@dsub.io">도움말</a>`
			if invalid {
				source = `<img alt="DSUB"><p>DSUB Help</p>`
				target = `<img alt="DSUB"><p>DSUB 도움말</p>`
			}
			source, err := email.CanonicalizeLayoutSourceMarkers(source)
			require.NoError(t, err)
			before := map[string]string{"en": source, "ko": target}
			after, err := email.BackfillLayoutMarkers("en", before)
			require.NoError(t, err)
			for locale, html := range after {
				after[locale] = EmailSource(html)
			}
			expectLayoutInventory(mock, before)
			expectBackupSchema(mock)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT "source_locale" FROM "email_layout".*FOR UPDATE`).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"source_locale"}).AddRow("en"))
			mock.ExpectQuery(`SELECT locale, html_content FROM "email_layout_translation".*FOR UPDATE`).WithArgs("legacy").WillReturnRows(sqlmock.NewRows([]string{"locale", "html_content"}).AddRow("en", before["en"]).AddRow("ko", before["ko"]))
			run := &capturedRun{}
			mock.ExpectExec(`INSERT INTO site_variable_migration_backup.*'email_layout'`).WithArgs(run, "email_layout:legacy", "legacy").WillReturnResult(sqlmock.NewResult(1, 1))
			for _, locale := range []string{"en", "ko"} {
				key := fmt.Sprintf(`{"entity_id":"legacy","locale":%q}`, locale)
				mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "email_layout_translation".*FOR UPDATE`).WithArgs(key).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(layoutRow(locale, before[locale])))
				mock.ExpectExec(`INSERT INTO site_variable_migration_backup.*VALUES`).WithArgs(run, "email_layout:legacy", "email_layout_translation", key, layoutRow(locale, before[locale])).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec(`UPDATE "email_layout_translation" SET "html_content"`).WithArgs(after[locale], sqlmock.AnyArg(), sqlmock.AnyArg(), before[locale]).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			expectLayoutValidation(mock, after, invalid)
			if invalid {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			var report bytes.Buffer
			require.NoError(t, Run(t.Context(), db, true, &report))
			require.Contains(t, report.String(), "run_id="+run.id)
			if invalid {
				require.Contains(t, report.String(), "skipped owner=email_layout:legacy")
				require.Contains(t, report.String(), "MISSING_CONTENT_PLACEHOLDER")
			} else {
				require.NotContains(t, report.String(), "skipped")
				expectLayoutInventory(mock, after)
				report.Reset()
				require.NoError(t, Run(t.Context(), db, true, &report))
				require.Equal(t, "owners=0 apply=true\n", report.String())
				require.Contains(t, after["ko"], "도움말")
				require.Contains(t, after["ko"], "mailto:{{contact.email}}")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
