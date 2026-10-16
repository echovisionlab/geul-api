package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestContactCutoverAuditsImmutableDeliverySnapshotsWithoutRewritingThem(t *testing.T) {
	for _, status := range []string{"scheduled", "sending", "unknown", "sent", "failed", "cancelled", "skipped"} {
		t.Run(status, func(t *testing.T) {
			db, mock := migrationTestDB(t)
			for _, spec := range contactTables()[:len(contactTables())-2] {
				mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			}
			mock.ExpectQuery("SELECT EXISTS").WithArgs(contactDeliveryAudit.table, contactDeliveryAudit.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			row, err := json.Marshal(map[string]any{"id": "run-1", "status": status, "definition_sealed": true, "render_snapshot": map[string]any{"subject": "{{\ncontact.privacyEmail\n}}", "content_html": "<p>Archive</p>"}})
			require.NoError(t, err)
			mock.ExpectQuery(`SELECT jsonb_build_object.*FROM "email_delivery_run"`).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
			mock.ExpectQuery("SELECT EXISTS").WithArgs(contactTranslationAudit.table, contactTranslationAudit.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			var report bytes.Buffer
			err = RunContactEmail(t.Context(), db, true, &report)
			if status == "scheduled" || status == "sending" || status == "unknown" {
				require.ErrorContains(t, err, "active immutable delivery snapshots")
				require.Equal(t, "target_catalog="+contactTargetCatalog+"\nmetadata_rows_blocking=0\ntranslation_jobs_blocking=0\ndelivery_runs_blocking=1 archives_preserved=0\n", report.String())
			} else {
				require.NoError(t, err)
				require.Equal(t, "target_catalog="+contactTargetCatalog+"\nmetadata_rows_blocking=0\ntranslation_jobs_blocking=0\ndelivery_runs_blocking=0 archives_preserved=1\nowners=0 apply=true\n", report.String())
			}
			// No backup schema, transaction, UPDATE or trigger bypass is permitted.
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
