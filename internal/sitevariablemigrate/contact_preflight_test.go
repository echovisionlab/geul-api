package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestContactCutoverPreservesProviderArtifactsAndBlocksOldQueuedInput(t *testing.T) {
	for _, input := range []struct{ manifest, xliff string }{
		{`{"key":"contact.supportEmail"}`, "<xliff/>"},
		{`{}`, "<source>{ { CONTACT.PRIVACYEMAIL } }</source>"},
		{`{"catalog":"7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49"}`, "<xliff/>"},
	} {
		db, mock := migrationTestDB(t)
		specs := contactTables()
		for _, spec := range specs[:len(specs)-1] {
			mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		}
		mock.ExpectQuery("SELECT EXISTS").WithArgs(contactTranslationAudit.table, contactTranslationAudit.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		row, err := json.Marshal(map[string]any{"status": "queued", "request_manifest": json.RawMessage(input.manifest), "request_xliff": []byte(input.xliff)})
		require.NoError(t, err)
		mock.ExpectQuery(`SELECT jsonb_build_object.*FROM "translation_job"`).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
		var report bytes.Buffer
		err = RunContactEmail(t.Context(), db, true, &report)
		require.ErrorContains(t, err, "active immutable translation artifacts")
		require.Contains(t, report.String(), "translation_jobs_blocking=1")
		require.NotContains(t, report.String(), "run_id=")
		require.NoError(t, mock.ExpectationsWereMet(), "provider handles, digest, manifest and XLIFF must never be mutated")
	}
}

func TestContactCutoverBlocksUnsupportedMetadataBeforeAnyWrites(t *testing.T) {
	db, mock := migrationTestDB(t)
	for _, spec := range contactTables() {
		exists := spec.table == "site_settings"
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
		if exists {
			mock.ExpectQuery(`SELECT jsonb_build_object.*FROM "site_settings"`).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(`{"meta_description":"{{contact.privacyEmail}}"}`))
		}
	}
	var report bytes.Buffer
	err := RunContactEmail(t.Context(), db, true, &report)
	require.ErrorContains(t, err, "outside the reviewed mutation allowlist")
	require.Contains(t, report.String(), "metadata_rows_blocking=1")
	require.NoError(t, mock.ExpectationsWereMet())
}
