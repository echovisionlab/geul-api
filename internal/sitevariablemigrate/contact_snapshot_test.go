package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func contactVersionFixture(kind, fingerprint string) string {
	document := map[string]any{"blockCatalogFingerprint": fingerprint, "locale": "en", "base": map[string]any{}, "localeOverlay": map[string]any{"locale": "en"}}
	if kind != "page" {
		document["profile"] = "RICH_TEXT_PROFILE_" + map[string]string{"post": "POST", "work": "WORK"}[kind]
		document["base"] = map[string]any{"nodes": []any{map[string]any{"block": map[string]any{"id": "10000000-0000-4000-8000-000000000001", "paragraph": map[string]any{"props": map[string]any{}}}, "placement": map[string]any{}}}}
		document["localeOverlay"] = map[string]any{"locale": "en", "blocks": []any{map[string]any{"blockId": "10000000-0000-4000-8000-000000000001", "paragraph": map[string]any{"props": map[string]any{}, "content": []any{
			map[string]any{"siteVariable": map[string]any{"key": "contact.supportEmail", "linked": true, "styles": map[string]any{"bold": true}}},
			map[string]any{"siteVariable": map[string]any{"key": "contact.privacyEmail", "styles": map[string]any{"italic": true}}},
			map[string]any{"siteVariable": map[string]any{"key": "contact.legalEmail"}},
		}}}}}
	}
	encoded, _ := json.Marshal(map[string]any{"schemaVersion": 1, "sourceLocale": "en", "title": "{{contact.supportEmail}}", "summary": nil, "document": document})
	return string(encoded)
}

func TestContactHistoryConvertsReviewedCatalogsBeforeStrictValidation(t *testing.T) {
	for _, kind := range []string{"page", "post", "work"} {
		for fingerprint := range contactSourceCatalogs {
			t.Run(kind+"/"+fingerprint[:8], func(t *testing.T) {
				before := contactVersionFixture(kind, fingerprint)
				after, err := ContactSnapshotJSON([]byte(before), kind)
				require.NoError(t, err)
				require.Contains(t, string(after), contactTargetCatalog)
				require.NotContains(t, string(after), "contact.supportEmail")
				require.NotContains(t, string(after), "contact.privacyEmail")
				if kind != "page" {
					require.Contains(t, string(after), `"styles":{"bold":true}`)
					require.Contains(t, string(after), `"styles":{"italic":true}`)
					require.Contains(t, string(after), `"key":"contact.legalEmail"`)
				}
				repeated, err := ContactSnapshotJSON(after, kind)
				require.NoError(t, err)
				require.Equal(t, after, repeated)
				require.Contains(t, before, fingerprint, "conversion must not replace the saved before-image")
			})
		}
	}
	for _, fingerprint := range []string{"", "stale-catalog"} {
		_, err := ContactSnapshotJSON([]byte(contactVersionFixture("post", fingerprint)), "post")
		require.ErrorContains(t, err, "unreviewed")
	}
	_, err := ContactSnapshotJSON([]byte(contactVersionFixture("post", contactTargetCatalog)), "work")
	require.ErrorContains(t, err, "profile")
}

func expectContactHistoryInventory(mock sqlmock.Sqlmock, source string) {
	for _, spec := range contactTables() {
		exists := spec.table == "post_version"
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
		if exists {
			mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "post_version"`).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(fmt.Sprintf(`{"id":"version-1","post_id":"post-1","version":3,"content_snapshot":%s}`, source)))
		}
	}
}

func TestContactHistoryApplyKeepsBeforeImageAndRejectsStaleRows(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint("conflict=", conflict), func(t *testing.T) {
			db, mock := migrationTestDB(t)
			source := contactVersionFixture("post", "7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49")
			expectContactHistoryInventory(mock, source)
			expectBackupSchema(mock)
			mock.ExpectBegin()
			row := fmt.Sprintf(`{"id":"version-1","post_id":"post-1","version":3,"content_snapshot":%s}`, source)
			mock.ExpectQuery(`SELECT to_jsonb\(t\).*FROM "post_version".*FOR UPDATE`).WithArgs(`{"id":"version-1"}`).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(row))
			mock.ExpectExec("INSERT INTO site_variable_migration_backup").WithArgs(sqlmock.AnyArg(), "post_version:version-1", "post_version", `{"id":"version-1"}`, row).WillReturnResult(sqlmock.NewResult(1, 1))
			rows := int64(1)
			if conflict {
				rows = 0
			}
			mock.ExpectExec(`UPDATE "post_version" SET "content_snapshot"`).WithArgs(sqlmock.AnyArg(), "version-1", source).WillReturnResult(sqlmock.NewResult(0, rows))
			if conflict {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			var report bytes.Buffer
			err := RunContactEmail(t.Context(), db, true, &report)
			if conflict {
				require.ErrorContains(t, err, "migration conflict")
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
			if !conflict {
				after, err := ContactSnapshotJSON([]byte(source), "post")
				require.NoError(t, err)
				expectContactHistoryInventory(mock, string(after))
				report.Reset()
				require.NoError(t, RunContactEmail(t.Context(), db, true, &report))
				require.Equal(t, "target_catalog="+contactTargetCatalog+"\nmetadata_rows_blocking=0\ntranslation_jobs_blocking=0\ndelivery_runs_blocking=0 archives_preserved=0\nowners=0 apply=true\n", report.String())
				require.NoError(t, mock.ExpectationsWereMet())
			}
		})
	}
}

func TestContactVerificationRequiresZeroRemainingHistoryChanges(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectContactHistoryInventory(mock, contactVersionFixture("post", "7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49"))
	var report bytes.Buffer
	err := VerifyContactEmail(t.Context(), db, &report)
	require.ErrorContains(t, err, "owners still require conversion")
	require.Contains(t, report.String(), "owners=1 apply=false")
	require.NoError(t, mock.ExpectationsWereMet())
}
