//go:build integration

package translation

import (
	"database/sql"
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestTargetCatalogMatchesCurrentSchemaIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		ApplyAppSchemaSQL: true,
	})
	for _, definition := range definitions {
		t.Run(string(definition.Kind), func(t *testing.T) {
			assertTranslationRelationExists(t, postgres.DB, definition.RootTable)
			assertTranslationRelationExists(t, postgres.DB, definition.EntryTable)
			assertTranslationRootAuthorityColumns(t, postgres.DB, definition)
		})
	}
	assertTranslationJobHardCutColumns(t, postgres.DB)
	assertRemovedLegacyRootHashes(t, postgres.DB)
}

func assertRemovedLegacyRootHashes(t *testing.T, db *gorm.DB) {
	t.Helper()
	for table, removed := range map[string][]string{
		"artist":        {"view_hash"},
		"page":          {"content_hash", "view_hash"},
		"post":          {"content_hash", "view_hash"},
		"program_event": {"content_hash", "view_hash"},
		"work":          {"content_hash", "view_hash"},
	} {
		var columns []string
		if err := db.Raw(
			`SELECT column_name
			 FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = ?`,
			table,
		).Scan(&columns).Error; err != nil {
			t.Fatalf("inspect %s legacy hash columns: %v", table, err)
		}
		for _, column := range removed {
			if containsColumn(columns, column) {
				t.Fatalf("%s still exposes removed column %s", table, column)
			}
		}
	}
}

func assertTranslationRelationExists(t *testing.T, db *gorm.DB, relationName string) {
	t.Helper()
	var relation sql.NullString
	if err := db.Raw("SELECT to_regclass(?)::text", "public."+relationName).Scan(&relation).Error; err != nil {
		t.Fatalf("resolve relation %s: %v", relationName, err)
	}
	if !relation.Valid || relation.String == "" {
		t.Fatalf("translation catalog relation %s does not exist", relationName)
	}
}

func assertTranslationRootAuthorityColumns(t *testing.T, db *gorm.DB, definition Definition) {
	t.Helper()
	var columns []string
	if err := db.Raw(
		`SELECT column_name
		 FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = ?`,
		definition.RootTable,
	).Scan(&columns).Error; err != nil {
		t.Fatalf("inspect %s authority columns: %v", definition.RootTable, err)
	}
	for _, required := range []string{"source_locale", "content_document_id"} {
		if !containsColumn(columns, required) {
			t.Fatalf("translation root %s is missing %s", definition.RootTable, required)
		}
	}
}

func assertTranslationJobHardCutColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	var columns []string
	if err := db.Raw(
		`SELECT column_name
		 FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = 'translation_job'`,
	).Scan(&columns).Error; err != nil {
		t.Fatalf("inspect translation_job columns: %v", err)
	}
	for _, required := range []string{"request_artifact_digest", "request_xliff", "request_manifest"} {
		if !containsColumn(columns, required) {
			t.Fatalf("translation_job is missing %s", required)
		}
	}
	for _, removed := range []string{
		"source_hash", "source_revision", "source_epoch", "translation_spec_version", "response_xliff",
		"response_manifest", "failure_reason", "completed_at", "cancel_requested",
	} {
		if containsColumn(columns, removed) {
			t.Fatalf("translation_job still exposes removed column %s", removed)
		}
	}
}

func containsColumn(columns []string, target string) bool {
	for _, column := range columns {
		if column == target {
			return true
		}
	}
	return false
}
