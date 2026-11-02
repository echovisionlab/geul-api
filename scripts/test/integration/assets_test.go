package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuiteAssetsPreflight(t *testing.T) {
	workspace := t.TempDir()
	repoRoot := filepath.Join(workspace, "geul-api")
	schemaRoot := filepath.Join(workspace, "geul-schema")
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Empty siblings keep ancestor workspace checkouts out of this fixture.
	for _, repo := range []string{"geul-identity", "geul-editor-collab", "geul-common", "geul-event-contracts", "geul-telemetry"} {
		if err := os.MkdirAll(filepath.Join(workspace, repo), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db, _ := selectedIntegrationBands("db", "")
	ory, _ := selectedIntegrationBands("ory", "")
	if err := requireSuiteAssets(repoRoot, schemaRoot, db); err == nil || !strings.Contains(err.Error(), "--schema-root") {
		t.Fatalf("missing schema error = %v", err)
	}
	write(filepath.Join(schemaRoot, "schema.sql"))
	if err := requireSuiteAssets(repoRoot, schemaRoot, db); err != nil {
		t.Fatalf("db requires backend assets: %v", err)
	}
	err := requireSuiteAssets(repoRoot, schemaRoot, ory)
	for _, missing := range []string{"geul-identity", "schema.generated.zed", "geul-editor-collab", "geul-common", "geul-event-contracts", "geul-telemetry"} {
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %s diagnostic: %v", missing, err)
		}
	}
	for _, asset := range []string{"go.mod", "config/spicedb/schema.generated.zed", "config/kratos/kratos.yml", "config/kratos/identity.schema.json", "config/oathkeeper/oathkeeper.yml", "config/oathkeeper/rules.yml"} {
		write(filepath.Join(workspace, "geul-identity", filepath.FromSlash(asset)))
	}
	for _, repo := range []string{"geul-editor-collab", "geul-common", "geul-event-contracts", "geul-telemetry"} {
		write(filepath.Join(workspace, repo, "package.json"))
	}
	write(filepath.Join(workspace, "geul-editor-collab", "tsconfig.local-dependencies.json"))
	if err := requireSuiteAssets(repoRoot, schemaRoot, ory); err != nil {
		t.Fatalf("prepared workspace: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(workspace, "geul-identity", "config", "spicedb")); err != nil {
		t.Fatal(err)
	}
	// A stale monorepo asset must not hide the missing canonical schema.
	write(filepath.Join(repoRoot, "infra", "spicedb", "schema.generated.zed"))
	if err := requireSuiteAssets(repoRoot, schemaRoot, ory); err == nil || !strings.Contains(err.Error(), "geul-identity") {
		t.Fatalf("missing canonical schema error = %v", err)
	}
}
