package sitevariablemigrate

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func migrationTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	connection, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db, mock
}

func expectBackupSchema(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS site_variable_migration_backup",
		"ALTER TABLE site_variable_migration_backup ADD COLUMN IF NOT EXISTS run_id text NOT NULL DEFAULT 'legacy'",
		"ALTER TABLE site_variable_migration_backup ALTER COLUMN run_id DROP DEFAULT",
		"ALTER TABLE site_variable_migration_backup DROP CONSTRAINT IF EXISTS site_variable_migration_backup_table_name_row_key_key",
		"CREATE UNIQUE INDEX IF NOT EXISTS site_variable_migration_backup_run_row_key ON site_variable_migration_backup(run_id, table_name, row_key)",
	} {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectCommit()
}
func expectMenuInventory(mock sqlmock.Sqlmock, href string) {
	expectMenuTableInventory(mock, "menu", href)
}

func menuInventoryRow(table, href string) string {
	if table == "menu_translation" {
		return fmt.Sprintf(`{"entity_id":"menu-1","locale":"en","items_json":[{"id":"stable","href":%q}]}`, href)
	}
	return fmt.Sprintf(`{"id":"menu-1","items":[{"id":"stable","href":%q}]}`, href)
}

func expectMenuTableInventory(mock sqlmock.Sqlmock, table, href string) {
	for _, spec := range tables {
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(spec.table == table))
		if spec.table == table {
			order := spec.owner
			if spec.locale != "" {
				order += ", " + spec.locale
			}
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_jsonb(t) AS row FROM "` + table + `" t ORDER BY ` + order)).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(menuInventoryRow(table, href)))
		}
	}
}
func TestMigrationDryRunAndRepeatRun(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectMenuInventory(mock, "https://www.dsub.io/path?x=1#a")
	var report bytes.Buffer
	require.NoError(t, Run(context.Background(), db, false, &report))
	require.Contains(t, report.String(), "menu.items locale= changes=1")
	require.Contains(t, report.String(), "owners=1 apply=false")
	require.NoError(t, mock.ExpectationsWereMet())
	// A subsequent run over the converted fixture performs no backup or UPDATE.
	expectMenuInventory(mock, "/path?x=1#a")
	report.Reset()
	require.NoError(t, Run(context.Background(), db, true, &report))
	require.Equal(t, "owners=0 apply=true\n", report.String())
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestMigrationApplyBacksUpAndRollsBackOwnerConflicts(t *testing.T) {
	for _, table := range []string{"menu", "menu_translation"} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/conflict=%t", table, conflict), func(t *testing.T) {
				db, mock := migrationTestDB(t)
				expectMenuTableInventory(mock, table, "https://www.dsub.io/path")
				expectBackupSchema(mock)
				mock.ExpectBegin()
				documentID, revision := uuid.NewString(), uuid.NewString()
				mock.ExpectQuery(`SELECT \* FROM "menu".*FOR UPDATE`).WithArgs("menu-1", 1).
					WillReturnRows(sqlmock.NewRows([]string{"id", "source_locale", "content_document_id"}).AddRow("menu-1", "en", documentID))
				mock.ExpectQuery(`SELECT profile, revision FROM "content_document".*FOR UPDATE`).WithArgs(documentID, 1).
					WillReturnRows(sqlmock.NewRows([]string{"profile", "revision"}).AddRow("compact", revision))
				mock.ExpectExec("INSERT INTO site_variable_migration_backup").WithArgs(sqlmock.AnyArg(), "menu:menu-1", "menu", "menu-1").WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec("INSERT INTO site_variable_migration_backup").WithArgs(sqlmock.AnyArg(), "menu:menu-1", "content_document", documentID).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_jsonb(t) AS row FROM "` + table + `"`)).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(menuInventoryRow(table, "https://www.dsub.io/path")))
				mock.ExpectExec("INSERT INTO site_variable_migration_backup").WillReturnResult(sqlmock.NewResult(1, 1))
				rows := int64(1)
				if conflict {
					rows = 0
				}
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE "` + table + `"`)).WillReturnResult(sqlmock.NewResult(0, rows))
				if conflict {
					mock.ExpectRollback()
				} else {
					mock.ExpectExec(regexp.QuoteMeta(`UPDATE "menu" SET "updated_at"=$1 WHERE id = $2`)).WithArgs(sqlmock.AnyArg(), "menu-1").WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(`UPDATE content_document SET revision = \$1, updated_at = \$2 WHERE id = \$3 AND revision = \$4`).
						WithArgs(newRevision{previous: revision}, sqlmock.AnyArg(), documentID, revision, "menu-1", documentID).
						WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
				}
				var report bytes.Buffer
				err := Run(context.Background(), db, true, &report)
				if conflict {
					require.ErrorContains(t, err, "migration conflict")
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, mock.ExpectationsWereMet())
				if !conflict {
					// Re-running the applied migration must not advance the revision again.
					expectMenuTableInventory(mock, table, "/path")
					report.Reset()
					require.NoError(t, Run(context.Background(), db, true, &report))
					require.Equal(t, "owners=0 apply=true\n", report.String())
					require.NoError(t, mock.ExpectationsWereMet())
				}
			})
		}
	}
}

type newRevision struct{ previous string }

func (m newRevision) Match(value driver.Value) bool {
	text, ok := value.(string)
	parsed, err := uuid.Parse(text)
	return ok && err == nil && parsed != uuid.Nil && text != m.previous
}

var _ sqlmock.Argument = newRevision{}
