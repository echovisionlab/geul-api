package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// Check local inputs before creating any containers or logical database leases.
// The db band deliberately has no Identity or Node checkout requirements.
func requireSuiteAssets(repoRoot, schemaRoot string, bands []integrationBand) error {
	var missing []error
	requireFile := func(path, hint string) {
		info, err := os.Stat(path)
		if err != nil {
			missing = append(missing, fmt.Errorf("required integration asset %s is unavailable (%s): %w", path, hint, err))
		} else if !info.Mode().IsRegular() {
			missing = append(missing, fmt.Errorf("required integration asset %s must be a regular file (%s)", path, hint))
		}
	}
	requireFile(filepath.Join(schemaRoot, "schema.sql"), "set --schema-root to the reviewed geul-schema checkout")
	if integrationBandsNeedBackend(bands) {
		identity := pgtest.AppIntegrationSiblingPath(repoRoot, "geul-identity")
		requireFile(filepath.Join(identity, "go.mod"), "prepare the sibling geul-identity checkout")
		requireFile(pgtest.AppIntegrationSpiceDBSchemaPath(repoRoot), "prepare Identity's canonical config/spicedb/schema.generated.zed artifact")
		for _, asset := range []string{"kratos/kratos.yml", "kratos/identity.schema.json", "oathkeeper/oathkeeper.yml", "oathkeeper/rules.yml"} {
			requireFile(filepath.Join(identity, "config", filepath.FromSlash(asset)), "prepare the sibling geul-identity checkout")
		}
		collab := pgtest.AppIntegrationSiblingPath(repoRoot, "geul-editor-collab")
		requireFile(filepath.Join(collab, "package.json"), "prepare the sibling geul-editor-collab checkout")
		requireFile(filepath.Join(collab, "tsconfig.local-dependencies.json"), "prepare collaboration's local source configuration")
		// These paths are relative to collaboration's tsconfig, not the API checkout.
		for _, sibling := range []string{"geul-common", "geul-event-contracts", "geul-telemetry"} {
			requireFile(filepath.Join(filepath.Dir(collab), sibling, "package.json"), "prepare the sibling "+sibling+" checkout required by collaboration")
		}
	}
	return errors.Join(missing...)
}
