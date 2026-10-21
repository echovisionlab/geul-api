package displaysizemigrate

import (
	"bytes"
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const documentID = "30000000-0000-4000-8000-000000000003"

var (
	liveBefore = `{"file":{"props":{"attachment":{"activeFileId":"` + fileID + `"},"previewWidth":47}}}`
	liveAfter  = `{"file":{"props":{"attachment":{"activeFileId":"` + fileID + `"},"displaySize":47}}}`
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

func expectInventory(mock sqlmock.Sqlmock, snapshot string) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM content_block_locale`)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT b.id, b.document_id`)).WillReturnRows(sqlmock.NewRows([]string{"id", "document_id", "kind", "profile", "shared_data"}).AddRow(blockID, documentID, "file", "post", liveBefore))
	for _, version := range versionTables {
		rows := sqlmock.NewRows([]string{"id", "content_snapshot"})
		if version.table == "post_version" {
			rows.AddRow("40000000-0000-4000-8000-000000000004", snapshot)
		}
		mock.ExpectQuery(regexp.QuoteMeta(`FROM "` + version.table + `"`)).WillReturnRows(rows)
	}
}

func expectBackupSchema(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS display_size_migration_backup`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE UNIQUE INDEX IF NOT EXISTS display_size_migration_backup_run_row_key`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

func expectBackup(mock sqlmock.Sqlmock, table, id string) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_jsonb(t)::text AS row FROM "` + table + `" t WHERE id = $1 FOR UPDATE`)).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(`{"id":"` + id + `"}`))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO display_size_migration_backup`)).WithArgs(sqlmock.AnyArg(), table, `{"id":"`+id+`"}`, `{"id":"`+id+`"}`).WillReturnResult(sqlmock.NewResult(1, 1))
}

func TestRunReportsWithoutWritingUnlessApplied(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectInventory(mock, versionFixture("post", "732d62ad223d3012770b10d0a1ab25a8dae2336c52b56ecd01cf276051e75637", legacyField))
	var report bytes.Buffer

	require.NoError(t, Run(context.Background(), db, false, &report))

	require.Equal(t, "target_catalog="+targetCatalog+"\ncontent_block changes=1 documents=1\nversion_snapshot changes=1 apply=false\n", report.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunBacksUpEachRowBeforeRewritingIt(t *testing.T) {
	db, mock := migrationTestDB(t)
	snapshot := versionFixture("post", "732d62ad223d3012770b10d0a1ab25a8dae2336c52b56ecd01cf276051e75637", legacyField)
	expectInventory(mock, snapshot)
	expectBackupSchema(mock)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT id FROM content_document WHERE id = $1 FOR UPDATE`)).WithArgs(documentID).WillReturnResult(sqlmock.NewResult(0, 1))
	expectBackup(mock, "content_block", blockID)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "content_block" SET "shared_data" = $1::jsonb WHERE id = $2 AND "shared_data" = $3::jsonb`)).WithArgs(liveAfter, blockID, liveBefore).WillReturnResult(sqlmock.NewResult(0, 1))
	expectBackup(mock, "content_document", documentID)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "content_document" SET "revision"=$1 WHERE id = $2`)).WithArgs(sqlmock.AnyArg(), documentID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectBackup(mock, "post_version", "40000000-0000-4000-8000-000000000004")
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "post_version" SET "content_snapshot"`)).WithArgs(sqlmock.AnyArg(), "40000000-0000-4000-8000-000000000004", snapshot).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	var report bytes.Buffer

	require.NoError(t, Run(context.Background(), db, true, &report))

	require.Contains(t, report.String(), "apply=true\nrun_id=")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRunRejectsARowThatChangedAfterPlanning(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectInventory(mock, versionFixture("post", targetCatalog, targetField))
	expectBackupSchema(mock)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT id FROM content_document`)).WithArgs(documentID).WillReturnResult(sqlmock.NewResult(0, 1))
	expectBackup(mock, "content_block", blockID)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "content_block"`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := Run(context.Background(), db, true, &bytes.Buffer{})

	require.ErrorContains(t, err, "changed after planning")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestVerifyFailsWhileRowsRemain(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectInventory(mock, versionFixture("post", targetCatalog, targetField))

	err := Verify(context.Background(), db, &bytes.Buffer{})

	require.ErrorContains(t, err, "1 rows still require conversion")
	require.NoError(t, mock.ExpectationsWereMet())
}
