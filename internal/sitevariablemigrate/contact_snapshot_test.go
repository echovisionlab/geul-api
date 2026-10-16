package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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
	_, err := ContactSnapshotJSON([]byte(contactVersionFixture("post", strings.Repeat("a", 64))), "post")
	require.ErrorContains(t, err, "unreviewed")
	invalidShape := strings.Replace(contactVersionFixture("post", "5afc20ddc15f7c6099873f33dae6557c5f263e668f12e0babb0186f051a52977"), `"paragraph":{"props":{}}`, `"paragraph":{"props":{"unreviewedField":true}}`, 1)
	_, err = ContactSnapshotJSON([]byte(invalidShape), "post")
	require.Error(t, err, "an observed fingerprint must never bypass current shape validation")
	_, err = ContactSnapshotJSON([]byte(contactVersionFixture("post", contactTargetCatalog)), "work")
	require.ErrorContains(t, err, "profile")
}

func TestContactSnapshotDeltaPreservesUnrelatedPayload(t *testing.T) {
	before := []byte(`{"schemaVersion":1,"sourceLocale":"en","title":"Title {{contact.supportEmail}}","summary":null,"document":{"blockCatalogFingerprint":"1b0b80933dd4e3a92f66d8f85d25c915d74410ee47bb6cf6ec3adffcfe964bc8","nodes":[{"id":"first","text":"Before {{contact.privacyEmail}} after","styles":{"bold":true},"number":9007199254740993},{"id":"second","href":"mailto:{{contact.supportEmail}}?subject=hello"}]}}`)
	after := []byte(strings.NewReplacer("1b0b80933dd4e3a92f66d8f85d25c915d74410ee47bb6cf6ec3adffcfe964bc8", contactTargetCatalog, "contact.supportEmail", "contact.email", "contact.privacyEmail", "contact.email").Replace(string(before)))
	require.NoError(t, validateContactSnapshotDelta(before, after))
	for name, edit := range map[string][2]string{
		"id":             {`"id":"first"`, `"id":"changed"`},
		"style":          {`"bold":true`, `"bold":false`},
		"numeric_value":  {`9007199254740993`, `9007199254740992`},
		"title_literal":  {`Title `, `Other `},
		"text_literal":   {`Before `, `Other `},
		"href_parameter": {`?subject=hello`, `?subject=changed`},
		"missing_key":    {`"summary":null,`, ``},
	} {
		t.Run(name, func(t *testing.T) {
			changed := []byte(strings.Replace(string(after), edit[0], edit[1], 1))
			require.ErrorContains(t, validateContactSnapshotDelta(before, changed), "unrelated data")
		})
	}
	var reordered map[string]any
	decoder := json.NewDecoder(bytes.NewReader(after))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&reordered))
	nodes := reordered["document"].(map[string]any)["nodes"].([]any)
	nodes[0], nodes[1] = nodes[1], nodes[0]
	changedOrder, err := json.Marshal(reordered)
	require.NoError(t, err)
	require.ErrorContains(t, validateContactSnapshotDelta(before, changedOrder), "unrelated data")
	// Contact JSON must preserve large integers, not round through float64.
	preserved, err := ContactRichJSON([]byte(`{"count":9007199254740993,"text":"{{contact.supportEmail}}"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"count":9007199254740993,"text":"{{contact.email}}"}`, string(preserved))
	require.Contains(t, string(preserved), "9007199254740993")
	_, err = ContactRichJSON([]byte(`{} {}`))
	require.Error(t, err)
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
