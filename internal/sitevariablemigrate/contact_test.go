package sitevariablemigrate

import (
	"bytes"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContactRichMigrationPreservesDistinctAtomsStylesAndExecutableSource(t *testing.T) {
	source := `{"content":[{"siteVariable":{"key":"contact.supportEmail","linked":true,"styles":{"bold":true}}},{"siteVariable":{"key":"contact.privacyEmail","styles":{"italic":true}}},{"siteVariable":{"key":"contact.legalEmail"}},{"text":{"text":"{{contact.supportEmail}} / DSUB / support@dsub.io"}},{"link":{"href":"mailto:{{contact.privacyEmail}}","content":[{"text":"Ask {{contact.supportEmail}}","styles":{"bold":true}}]}}],"source":"{{contact.supportEmail}}","metadata":{"key":"contact.privacyEmail"}}`
	want := `{"content":[{"siteVariable":{"key":"contact.email","linked":true,"styles":{"bold":true}}},{"siteVariable":{"key":"contact.email","styles":{"italic":true}}},{"siteVariable":{"key":"contact.legalEmail"}},{"text":{"text":"{{contact.email}} / DSUB / support@dsub.io"}},{"link":{"href":"mailto:{{contact.email}}","content":[{"text":"Ask {{contact.email}}","styles":{"bold":true}}]}}],"source":"{{contact.supportEmail}}","metadata":{"key":"contact.privacyEmail"}}`
	converted, err := ContactRichJSON([]byte(source))
	require.NoError(t, err)
	require.JSONEq(t, want, string(converted))
	repeated, err := ContactRichJSON(converted)
	require.NoError(t, err)
	require.Equal(t, converted, repeated)
	_, err = ContactRichJSON([]byte(`{`))
	require.Error(t, err)
}

func TestContactMenuAndTemplateMigrationOnlyRewritesReservedTokens(t *testing.T) {
	for _, source := range []string{"{{\n contact.supportEmail \n}}", "{ { CONTACT.SUPPORTEMAIL } }", "{{contact.privacyemail}}"} {
		require.Equal(t, "{{contact.email}}", ContactEmailText(source))
	}
	source := `<p data-layout-unit="stable">DSUB {{contact.supportEmail}} / {{contact.privacyEmail}} / {{contact.legalEmail}} / {{recipient.email}}</p>`
	require.Equal(t, `<p data-layout-unit="stable">DSUB {{contact.email}} / {{contact.email}} / {{contact.legalEmail}} / {{recipient.email}}</p>`, ContactText(source))
	converted, err := ContactMenuJSON([]byte(`[{"id":"stable","label":"{{contact.supportEmail}}","href":"mailto:{{contact.privacyEmail}}","children":[{"label":"Legal","href":"mailto:{{contact.legalEmail}}"}]}]`))
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"stable","label":"{{contact.email}}","href":"mailto:{{contact.email}}","children":[{"label":"Legal","href":"mailto:{{contact.legalEmail}}"}]}]`, string(converted))
}

func TestContactMigrationDoesNotBackfillUnrelatedLayoutMarkersOrBranding(t *testing.T) {
	db, mock := migrationTestDB(t)
	expectLayoutInventory(mock, map[string]string{"en": `<p>DSUB</p>{{content}}`, "ko": `<p>DSUB</p>{{content}}`})
	expectNoContactHistory(mock)
	var report bytes.Buffer
	require.NoError(t, RunContactEmail(t.Context(), db, true, &report))
	require.Equal(t, "target_catalog="+contactTargetCatalog+"\nmetadata_rows_blocking=0\ntranslation_jobs_blocking=0\ndelivery_runs_blocking=0 archives_preserved=0\nowners=0 apply=true\n", report.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContactMigrationKeepsMenuOwnerBackupsRevisionAndConflictFence(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint("conflict=", conflict), func(t *testing.T) {
			db, mock := migrationTestDB(t)
			before, after := "mailto:{{contact.supportEmail}}", "mailto:{{contact.email}}"
			expectMenuInventory(mock, before)
			expectNoContactHistory(mock)
			expectBackupSchema(mock)
			mock.ExpectBegin()
			documentID, revision := uuid.NewString(), uuid.NewString()
			mock.ExpectQuery(`SELECT \* FROM "menu".*FOR UPDATE`).WithArgs("menu-1", 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "source_locale", "content_document_id"}).AddRow("menu-1", "en", documentID))
			mock.ExpectQuery(`SELECT profile, revision FROM "content_document".*FOR UPDATE`).WithArgs(documentID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"profile", "revision"}).AddRow("compact", revision))
			mock.ExpectExec("INSERT INTO site_variable_migration_backup").WithArgs(sqlmock.AnyArg(), "menu:menu-1", "menu", "menu-1").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("INSERT INTO site_variable_migration_backup").WithArgs(sqlmock.AnyArg(), "menu:menu-1", "content_document", documentID).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT to_jsonb(t) AS row FROM "menu"`)).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(menuInventoryRow("menu", before)))
			mock.ExpectExec("INSERT INTO site_variable_migration_backup").WillReturnResult(sqlmock.NewResult(1, 1))
			rows := int64(1)
			if conflict {
				rows = 0
			}
			mock.ExpectExec(`UPDATE "menu"`).WillReturnResult(sqlmock.NewResult(0, rows))
			if conflict {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE "menu" SET "updated_at"=$1 WHERE id = $2`)).WithArgs(sqlmock.AnyArg(), "menu-1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE content_document SET revision = \$1, updated_at = \$2 WHERE id = \$3 AND revision = \$4`).WithArgs(newRevision{previous: revision}, sqlmock.AnyArg(), documentID, revision, "menu-1", documentID).WillReturnResult(sqlmock.NewResult(0, 1))
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
				expectMenuInventory(mock, after)
				expectNoContactHistory(mock)
				report.Reset()
				require.NoError(t, RunContactEmail(t.Context(), db, true, &report))
				require.Equal(t, "target_catalog="+contactTargetCatalog+"\nmetadata_rows_blocking=0\ntranslation_jobs_blocking=0\ndelivery_runs_blocking=0 archives_preserved=0\nowners=0 apply=true\n", report.String())
				require.NoError(t, mock.ExpectationsWereMet())
			}
		})
	}
}

func expectNoContactHistory(mock sqlmock.Sqlmock) {
	for _, spec := range append(append([]tableSpec(nil), contactTextTables...), contactHistoryTables...) {
		mock.ExpectQuery("SELECT EXISTS").WithArgs(spec.table, spec.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}
	mock.ExpectQuery("SELECT EXISTS").WithArgs(contactDeliveryAudit.table, contactDeliveryAudit.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(contactTranslationAudit.table, contactTranslationAudit.column).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
}
