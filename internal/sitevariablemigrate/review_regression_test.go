package sitevariablemigrate

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type capturedRun struct{ id string }

func (run *capturedRun) Match(value driver.Value) bool {
	id, ok := value.(string)
	parsed, err := uuid.Parse(id)
	if !ok || err != nil || parsed == uuid.Nil {
		return false
	}
	if run.id == "" {
		run.id = id
	}
	return run.id == id
}

func TestApplyStoresFreshBeforeImagesInEachRun(t *testing.T) {
	db, mock := migrationTestDB(t)
	var previousRun string
	for _, summary := range []string{"DSUB", "DSUB updated"} {
		row := fmt.Sprintf(`{"entity_id":"page-1","locale":"en","summary":%q}`, summary)
		for _, spec := range tables {
			mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(spec.table == "page_translation"))
			if spec.table == "page_translation" {
				mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "page_translation"`).
					WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
				mock.ExpectQuery(`SELECT "?content_document_id"? FROM "page"`).WithArgs("page-1").
					WillReturnRows(sqlmock.NewRows([]string{"content_document_id"}).AddRow(nil))
			}
		}
		expectBackupSchema(mock)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "page_translation".*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
		run := &capturedRun{}
		// Assert both the run-scoped conflict target and this run's current row.
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) VALUES($1,$2,$3,$4::jsonb,$5::jsonb) ON CONFLICT(run_id,table_name,row_key) DO NOTHING`)).
			WithArgs(run, "page_translation:page-1", "page_translation", `{"entity_id":"page-1","locale":"en"}`, row).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec(`UPDATE "page_translation"`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		var report bytes.Buffer
		require.NoError(t, Run(context.Background(), db, true, &report))
		require.NotEmpty(t, run.id)
		require.NotEqual(t, previousRun, run.id)
		require.Contains(t, report.String(), "run_id="+run.id)
		previousRun = run.id
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestBackupSchemaUpgradeRollsBackOnFailure(t *testing.T) {
	db, mock := migrationTestDB(t)
	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS site_variable_migration_backup").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ALTER TABLE site_variable_migration_backup ADD COLUMN").WillReturnError(errors.New("upgrade failed"))
	mock.ExpectRollback()
	require.ErrorContains(t, ensureBackupTable(context.Background(), db), "upgrade failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func emailRow(owner, html string) string {
	return fmt.Sprintf(`{"entity_id":%q,"locale":"en","subject":"DSUB","content_html":%q}`, owner, html)
}

func expectEmailInventory(mock sqlmock.Sqlmock, rows []string) {
	for _, spec := range tables {
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(spec.table == "email_template_translation"))
		if spec.table != "email_template_translation" {
			continue
		}
		result := sqlmock.NewRows([]string{"row"})
		for _, row := range rows {
			result.AddRow(row)
		}
		mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "email_template_translation"`).WillReturnRows(result)
		for _, row := range rows {
			var fields map[string]string
			_ = json.Unmarshal([]byte(row), &fields)
			mock.ExpectQuery(`SELECT "?content_document_id"? FROM "email_template"`).WithArgs(fields["entity_id"]).
				WillReturnRows(sqlmock.NewRows([]string{"content_document_id"}).AddRow(nil))
		}
	}
}

func expectEmailSettings(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT .* FROM "site_settings"`).
		WillReturnRows(sqlmock.NewRows([]string{"site_title", "company_name", "legal_email", "support_email", "privacy_email"}).
			AddRow("rawsector", "Company", "legal@rawsector.com", "support@rawsector.com", "privacy@rawsector.com"))
}

func TestEmailApplyValidatesInsideTransactionAndSkipsInvalidOwner(t *testing.T) {
	t.Setenv("SITE_ORIGIN", "https://www.rawsector.com")
	db, mock := migrationTestDB(t)
	badHTML := `<a href="mailto:{{contact.legalEmail}}?subject=Help">DSUB</a>`
	goodHTML := `<a href="mailto:legal@dsub.io?subject=Help&amp;cc=support@dsub.io">DSUB</a>`
	expectEmailInventory(mock, []string{emailRow("a-invalid", badHTML), emailRow("b-valid", goodHTML)})
	expectBackupSchema(mock)
	run := &capturedRun{}
	for _, fixture := range []struct {
		owner, html string
		valid       bool
	}{{"a-invalid", badHTML, false}, {"b-valid", goodHTML, true}} {
		mock.ExpectBegin()
		row := emailRow(fixture.owner, fixture.html)
		key := fmt.Sprintf(`{"entity_id":%q,"locale":"en"}`, fixture.owner)
		for _, column := range []string{"subject", "content_html"} {
			mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "email_template_translation".*FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
			mock.ExpectExec(`INSERT INTO site_variable_migration_backup.*ON CONFLICT\(run_id,table_name,row_key\) DO NOTHING`).
				WithArgs(run, "email_template_translation:"+fixture.owner, "email_template_translation", key, row).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(`UPDATE "email_template_translation" SET "` + column + `"`).WillReturnResult(sqlmock.NewResult(0, 1))
			// The second column sees the modified row; its conflict must preserve
			// the first before-image for the shared row in this run.
			row = fmt.Sprintf(`{"entity_id":%q,"locale":"en","subject":"{{site.title}}","content_html":%q}`, fixture.owner, fixture.html)
		}
		expectEmailSettings(mock)
		mock.ExpectQuery(`SELECT subject, content_html FROM "email_template_translation"`).WithArgs(fixture.owner, "en", 1).
			WillReturnRows(sqlmock.NewRows([]string{"subject", "content_html"}).AddRow("{{site.title}}", EmailSource(fixture.html)))
		mock.ExpectQuery(`SELECT "layout_id" FROM "email_template"`).WithArgs(fixture.owner, 1).
			WillReturnRows(sqlmock.NewRows([]string{"layout_id"}).AddRow(nil))
		if fixture.valid {
			mock.ExpectCommit()
		} else {
			mock.ExpectRollback()
		}
	}
	var report bytes.Buffer
	require.NoError(t, Run(context.Background(), db, true, &report))
	require.Contains(t, report.String(), "skipped owner=email_template_translation:a-invalid")
	require.Contains(t, report.String(), "invalid site variable href")
	require.NotContains(t, report.String(), "skipped owner=email_template_translation:b-valid")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMigratedLayoutUsesRealValidationAndRenderer(t *testing.T) {
	t.Setenv("SITE_ORIGIN", "https://www.rawsector.com")
	for _, fixture := range []struct {
		name, html, errorText string
	}{
		{"valid query", `<header>{{site.title}}</header>{{content}}<a href="mailto:legal@rawsector.com?subject=Help&amp;cc=support@rawsector.com">Contact</a>`, ""},
		{"missing content", `<p>{{site.title}}</p>`, "MISSING_CONTENT_PLACEHOLDER"},
		{"invalid token href", `{{content}}<a href="mailto:{{contact.legalEmail}}?subject=Help">Contact</a>`, "invalid site variable href"},
		{"rendered unsafe href", `{{content}}<a href="mailto:legal@rawsector.com?subject=%0d%0aBcc:evil@example.test">Contact</a>`, "unsafe rendered email href"},
		{"unknown reserved name", `{{content}}<p>{{site.missing}}</p>`, "unknown site variable"},
		{"incorrect reserved case", `{{content}}<p>{{site.Title}}</p>`, "unknown site variable"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db, mock := migrationTestDB(t)
			if fixture.errorText == "" {
				var err error
				fixture.html, err = email.CanonicalizeLayoutSourceMarkers(fixture.html)
				require.NoError(t, err)
			}
			mock.ExpectBegin()
			expectEmailSettings(mock)
			mock.ExpectQuery(`SELECT "html_content" FROM "email_layout_translation"`).WithArgs("layout-1", "en", 1).
				WillReturnRows(sqlmock.NewRows([]string{"html_content"}).AddRow(fixture.html))
			if fixture.errorText == "" {
				mock.ExpectQuery(`SELECT root.source_locale, source.html_content FROM email_layout AS root`).WithArgs("layout-1", 1).
					WillReturnRows(sqlmock.NewRows([]string{"source_locale", "html_content"}).AddRow("en", fixture.html))
				mock.ExpectQuery(`SELECT source_locale AS locale FROM "email_layout"`).WithArgs("layout-1", 1).
					WillReturnRows(sqlmock.NewRows([]string{"locale"}).AddRow("en"))
				mock.ExpectQuery(`SELECT "html_content" FROM "email_layout_translation"`).WithArgs("layout-1", "en", 1).
					WillReturnRows(sqlmock.NewRows([]string{"html_content"}).AddRow(fixture.html))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				return validateMigratedEmails(context.Background(), tx, []change{{
					spec: tableSpec{table: "email_layout_translation"}, owner: "layout-1", locale: "en",
					key: json.RawMessage(`{"entity_id":"layout-1","locale":"en"}`),
				}})
			})
			if fixture.errorText == "" {
				require.NoError(t, err)
			} else {
				var invalid *emailValidationError
				require.ErrorAs(t, err, &invalid)
				require.ErrorContains(t, err, fixture.errorText)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSubjectOnlyMigrationValidatesUnchangedHTML(t *testing.T) {
	db, mock := migrationTestDB(t)
	mock.ExpectBegin()
	expectEmailSettings(mock)
	mock.ExpectQuery(`SELECT subject, content_html FROM "email_template_translation"`).WithArgs("template-1", "en", 1).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "content_html"}).AddRow("{{site.title}}", `<a href="javascript:alert(1)">Old body</a>`))
	mock.ExpectQuery(`SELECT "layout_id" FROM "email_template"`).WithArgs("template-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"layout_id"}).AddRow(nil))
	mock.ExpectRollback()
	err := db.Transaction(func(tx *gorm.DB) error {
		return validateMigratedEmails(context.Background(), tx, []change{{
			spec: tableSpec{table: "email_template_translation", column: "subject"}, owner: "template-1", locale: "en",
		}})
	})
	var invalid *emailValidationError
	require.ErrorAs(t, err, &invalid)
	require.ErrorContains(t, err, "unsafe rendered email href")
	require.NoError(t, mock.ExpectationsWereMet())
}
