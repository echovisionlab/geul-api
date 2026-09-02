//go:build integration

package testutil

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAppIntegrationSchemaSQLFilesRequireReviewedAssetRoot(t *testing.T) {
	_, err := appIntegrationSchemaSQLFiles("")
	require.ErrorContains(t, err, "schema root")
}

func TestSplitRepoPathForLegacySuffix(t *testing.T) {
	moduleRoot := filepath.Join(string(filepath.Separator), "workspace", "api")
	tests := map[string]string{
		"apps/collab":                        filepath.Join(string(filepath.Separator), "workspace", "geul-editor-collab"),
		"apps/web/.next":                     filepath.Join(string(filepath.Separator), "workspace", "geul-web", ".next"),
		"apps/transcoder":                    filepath.Join(string(filepath.Separator), "workspace", "geul-transcoder"),
		"infra/kratos/kratos.yml":            filepath.Join(string(filepath.Separator), "workspace", "geul-identity", "config", "kratos", "kratos.yml"),
		"infra/oathkeeper/rules.yml":         filepath.Join(string(filepath.Separator), "workspace", "geul-identity", "config", "oathkeeper", "rules.yml"),
		"infra/spicedb/schema.generated.zed": filepath.Join(string(filepath.Separator), "workspace", "geul-identity", "config", "spicedb", "schema.generated.zed"),
	}
	for suffix, expected := range tests {
		actual, ok := splitRepoPathForLegacySuffix(moduleRoot, suffix)
		require.True(t, ok)
		require.Equal(t, expected, actual)
	}
	_, ok := splitRepoPathForLegacySuffix(moduleRoot, "infra/unknown")
	require.False(t, ok)
}

func TestSplitRepoPathForLegacySuffixFindsSiblingRepoAboveWorktree(t *testing.T) {
	workspaceRoot := t.TempDir()
	moduleRoot := filepath.Join(workspaceRoot, ".worktrees", "api-account")
	kratosConfig := filepath.Join(workspaceRoot, "geul-identity", "config", "kratos", "kratos.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(kratosConfig), 0o755))
	require.NoError(t, os.WriteFile(kratosConfig, nil, 0o644))

	actual, ok := splitRepoPathForLegacySuffix(moduleRoot, "infra/kratos/kratos.yml")
	require.True(t, ok)
	require.Equal(t, kratosConfig, actual)
}

func TestAppIntegrationSchemaSQLFilesUseSingleSchemaAuthority(t *testing.T) {
	schemaRoot := t.TempDir()
	schemaFile := filepath.Join(schemaRoot, filepath.FromSlash(appIntegrationSchemaSQLRelativePath))
	require.NoError(t, os.WriteFile(schemaFile, nil, 0o644))
	files, err := appIntegrationSchemaSQLFiles(schemaRoot)
	require.NoError(t, err)
	require.Equal(t, []string{schemaFile}, files)
}

func TestAppIntegrationPostgresInitScriptFileIsOptionalByDefault(t *testing.T) {
	_, ok, err := appIntegrationPostgresInitScriptFile("")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestAppIntegrationPostgresInitScriptFileUsesSeparateAssetRoot(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "init", "02-extensions.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(scriptPath), 0o755))
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o644))
	got, ok, err := appIntegrationPostgresInitScriptFile(root)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, scriptPath, got)
}

func TestStartAppPostgresOrchestratedRequiresAdminLease(t *testing.T) {
	previous := *appIntegrationLeaseFile
	previousArgs := os.Args
	*appIntegrationLeaseFile = ""
	os.Args = withoutAppIntegrationLeaseArguments(previousArgs)
	t.Cleanup(func() {
		*appIntegrationLeaseFile = previous
		os.Args = previousArgs
	})

	_, _, err := StartAppPostgres(t.Context(), AppPostgresOptions{})
	require.ErrorContains(t, err, "-geul-integration-lease-file")
}

func withoutAppIntegrationLeaseArguments(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(args))
	filtered = append(filtered, args[0])
	for index := 1; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, "-geul-integration-lease-file=") {
			continue
		}
		if argument == "-geul-integration-lease-file" {
			if index+1 < len(args) {
				index++
			}
			continue
		}
		filtered = append(filtered, argument)
	}
	return filtered
}

func TestAppIntegrationCleanupContextIsBounded(t *testing.T) {
	ctx, cancel := appIntegrationCleanupContext()
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(appIntegrationCleanupTimeout), deadline, time.Second)
}

func TestWaitForAppPostgresStopsOnCanceledContext(t *testing.T) {
	database, err := sql.Open("postgres", "postgres://test:test@127.0.0.1:1/geul?sslmode=disable")
	if database != nil {
		t.Cleanup(func() { require.NoError(t, database.Close()) })
	}
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan error, 1)
	go func() { done <- waitForAppPostgres(ctx, database) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waitForAppPostgres did not stop after context cancellation")
	}
}

func TestIntegrationProcessCleanupRegistryRunsOnceInLIFOOrder(t *testing.T) {
	integrationCleanupMu.Lock()
	registeredBefore := integrationRegisteredCleanups
	suiteBefore := integrationSuiteCleanups
	tempRootBefore := integrationSuiteTempRoot
	integrationRegisteredCleanups = nil
	integrationSuiteCleanups = nil
	integrationSuiteTempRoot = ""
	integrationCleanupMu.Unlock()
	defer func() {
		integrationCleanupMu.Lock()
		integrationRegisteredCleanups = registeredBefore
		integrationSuiteCleanups = suiteBefore
		integrationSuiteTempRoot = tempRootBefore
		integrationCleanupMu.Unlock()
	}()

	var calls []string
	RegisterIntegrationProcessCleanup("suite PostgreSQL", func() error {
		calls = append(calls, "postgres")
		return nil
	})
	RegisterIntegrationProcessCleanup("suite lease descriptor", func() error {
		calls = append(calls, "lease")
		return nil
	})
	integrationCleanupMu.Lock()
	registered := append([]*integrationCleanup(nil), integrationRegisteredCleanups...)
	integrationCleanupMu.Unlock()

	require.NoError(t, runIntegrationRegisteredCleanups())
	require.NoError(t, runIntegrationCleanups(registered))
	require.NoError(t, runIntegrationRegisteredCleanups())
	require.Equal(t, []string{"lease", "postgres"}, calls)
}

func TestStartAppPostgresAdminLeasesAreDistinctAndDropped(t *testing.T) {
	if strings.TrimSpace(*appIntegrationLeaseFile) == "" {
		t.Skip("integration orchestrator admin DSN is required")
	}
	lease, err := loadAppIntegrationLease(*appIntegrationLeaseFile)
	require.NoError(t, err)

	first, firstCleanup, err := StartAppPostgres(t.Context(), AppPostgresOptions{ApplyAppSchemaSQL: true})
	if firstCleanup != nil {
		t.Cleanup(func() { require.NoError(t, firstCleanup()) })
	}
	require.NoError(t, err)
	second, secondCleanup, err := StartAppPostgres(t.Context(), AppPostgresOptions{ApplyAppSchemaSQL: true})
	if secondCleanup != nil {
		t.Cleanup(func() { require.NoError(t, secondCleanup()) })
	}
	require.NoError(t, err)
	require.Equal(t, lease.PostgresContainerID, first.ContainerID)
	require.Equal(t, lease.PostgresContainerID, second.ContainerID)
	require.NotEqual(t, first.DatabaseName, second.DatabaseName)
	require.NotEqual(t, first.DSN, second.DSN)

	firstName := currentIntegrationDatabaseName(t, first.SQLDB)
	secondName := currentIntegrationDatabaseName(t, second.SQLDB)
	require.NotEqual(t, firstName, secondName)
	require.NoError(t, firstCleanup())
	require.NoError(t, secondCleanup())

	admin, err := sql.Open("postgres", lease.PostgresAdminDSN)
	if admin != nil {
		t.Cleanup(func() { require.NoError(t, admin.Close()) })
	}
	require.NoError(t, err)
	var remaining int
	require.NoError(t, admin.QueryRowContext(
		t.Context(),
		"SELECT count(*) FROM pg_database WHERE datname = ANY($1)",
		pq.Array([]string{firstName, secondName}),
	).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestAppPostgresInternalDSNUsesLeasedDatabase(t *testing.T) {
	postgres := &AppPostgres{DSN: "postgres://test:test@127.0.0.1:49152/geul_it_abc?sslmode=disable"}
	dsn, err := postgres.InternalDSN("postgres")
	require.NoError(t, err)
	require.Equal(t, "postgres://test:test@postgres:5432/geul_it_abc?sslmode=disable", dsn)
}

func currentIntegrationDatabaseName(t *testing.T, database *sql.DB) string {
	t.Helper()
	var name string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&name))
	return name
}

func TestAppIntegrationRequiredSchemaRelationsCoverMeshOptimization(t *testing.T) {
	require.Contains(t, appIntegrationRequiredSchemaRelations, "public.mesh_optimization_candidate")
}

func TestCurrentSchemaSQLAppliesAndExposesRequiredSchema(t *testing.T) {
	pg := SetupAppPostgres(t, AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})

	require.NoError(t, requireAppIntegrationSchemaContract(t.Context(), pg.SQLDB))
}
