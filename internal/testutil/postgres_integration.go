//go:build integration

package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const appIntegrationPostgresInitScriptRelativePath = "init/02-extensions.sh"

const appIntegrationSchemaSQLRelativePath = "schema.sql"

const appIntegrationCleanupTimeout = 30 * time.Second

var appIntegrationDatabaseNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var appIntegrationContainerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
var appIntegrationLeaseFile = flag.String("geul-integration-lease-file", "", "protected integration lease descriptor")

var appIntegrationRequiredSchemaRelations = []string{
	"public.account_email_change_request",
	"public.auth_code_issuance",
	"public.domain_audit",
	"public.member",
	"public.map_theme",
	"public.mesh_optimization_candidate",
	"public.newsletter_subscription",
	"public.security_access",
	"public.sitemap_snapshot",
}

type appIntegrationColumnContract struct {
	table  string
	column string
}

var appIntegrationRequiredSchemaColumns = []appIntegrationColumnContract{
	{table: "account_email_change_request", column: "previous_email_address"},
	{table: "account_email_change_request", column: "requested_email_address"},
	{table: "member", column: "available_emails"},
	{table: "member", column: "primary_email"},
	{table: "map_theme", column: "light_background_color"},
	{table: "map_theme", column: "dark_background_color"},
	{table: "file", column: "delete_requested_at"},
	{table: "post", column: "content_document_id"},
	{table: "page", column: "content_document_id"},
	{table: "work", column: "content_document_id"},
	{table: "program_event", column: "content_document_id"},
	{table: "content_document", column: "revision"},
	{table: "content_block_attachment", column: "selector_kind"},
	{table: "translation_job", column: "provider_document_id"},
	{table: "translation_job", column: "provider_document_key"},
	{table: "translation_job", column: "provider_document_submitted_at"},
	{table: "upload_session", column: "expected_current_file_id"},
}

var appIntegrationForbiddenSchemaRelations = []string{
	"public.account_email_projection",
	"public.account_email_change_transition",
	"public.artist_alias",
	"public.artist_member",
	"public.blocked_ip",
	"public.blocked_pattern",
	"public.form_webhook",
	"public.og_lifecycle_outbox",
	"public.translation_job_bundle_checkpoint",
	"public.translation_job_candidate",
	"public.deleted_identity_security_record",
	"public.map_theme_variant",
	"public.user",
	"public.user_email_change",
}

var appIntegrationForbiddenSchemaColumns = []appIntegrationColumnContract{
	{table: "email_delivery_run", column: "fanout_started_at"},
	{table: "email_delivery_run", column: "fanout_completed_at"},
	{table: "email_delivery_run", column: "fanout_error"},
	{table: "email_delivery_run", column: "queued_count"},
	{table: "media_generation", column: "deleted_at"},
	{table: "media_generation", column: "failed_at"},
	{table: "media_generation", column: "failure_reason"},
	{table: "page", column: "needs_cleanup"},
	{table: "post", column: "needs_cleanup"},
	{table: "program_event", column: "needs_cleanup"},
	{table: "upload_session", column: "document_scope"},
	{table: "upload_session", column: "document_locale"},
	{table: "translation_job", column: "retry_of_job_id"},
	{table: "waveform_job", column: "attempts"},
	{table: "waveform_job", column: "last_heartbeat"},
	{table: "work", column: "needs_cleanup"},
}

type AppPostgresOptions struct {
	Network             *testcontainers.DockerNetwork
	Aliases             []string
	BootstrapKratosStub bool
	ApplyAppSchemaSQL   bool
}

type AppPostgres struct {
	ContainerID  string
	DatabaseName string
	DSN          string
	SQLDB        *sql.DB
	DB           *gorm.DB
}

func SetupAppPostgres(t *testing.T, opts AppPostgresOptions) *AppPostgres {
	t.Helper()

	pg, cleanup, err := StartAppPostgres(t.Context(), opts)
	if cleanup != nil {
		registerIntegrationCleanup(t, "postgres", cleanup)
	}
	require.NoError(t, err)
	return pg
}

func StartAppPostgres(ctx context.Context, opts AppPostgresOptions) (*AppPostgres, func() error, error) {
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	if err != nil {
		return nil, nil, err
	}
	return startLeasedAppPostgres(ctx, lease, opts)
}

func currentAppIntegrationLeasePath() string {
	const name = "geul-integration-lease-file"
	prefix := "-" + name + "="
	for index, argument := range os.Args[1:] {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
		if argument == "-"+name && index+2 <= len(os.Args[1:]) {
			return os.Args[index+2]
		}
	}
	return *appIntegrationLeaseFile
}

func CurrentAppIntegrationBackendLease() (*AppIntegrationBackendLease, error) {
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	if err != nil {
		return nil, err
	}
	if lease.Backend == nil {
		return nil, fmt.Errorf("suite backend lease is required")
	}
	backend := *lease.Backend
	return &backend, nil
}

func connectSharedAppPostgres(ctx context.Context, lease AppIntegrationLeaseDescriptor) (*AppPostgres, func() error, error) {
	if lease.Backend == nil {
		return nil, nil, fmt.Errorf("suite backend lease is required")
	}
	// Application-facing integration handles use the same pgx database/sql
	// driver as the production GORM connection so PostgreSQL errors retain the
	// same concrete type at domain boundaries.
	sqlDB, err := sql.Open("pgx", lease.Backend.PostgresDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("open suite backend database: %w", err)
	}
	cleanup := sqlDB.Close
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("ping suite backend database: %w", err),
			cleanup(),
		)
	}
	database, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open suite backend database with gorm: %w", err),
			cleanup(),
		)
	}
	return &AppPostgres{
		ContainerID:  lease.PostgresContainerID,
		DatabaseName: lease.Backend.PostgresDatabaseName,
		DSN:          lease.Backend.PostgresDSN,
		SQLDB:        sqlDB,
		DB:           database,
	}, cleanup, nil
}

// PrepareAppIntegrationTemplate applies the reviewed initialization assets and
// application schema once to the orchestrator-owned template database.
func PrepareAppIntegrationTemplate(ctx context.Context, dsn, schemaRoot, postgresAssetRoot string) error {
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open integration template database: %w", err)
	}
	defer database.Close()

	if err := waitForAppPostgres(ctx, database); err != nil {
		return err
	}
	if err := applyAppIntegrationPostgresInitAssets(database, postgresAssetRoot); err != nil {
		return err
	}
	if err := applyAppIntegrationSchemaSQLFromRoot(database, schemaRoot); err != nil {
		return err
	}
	return requireAppIntegrationSchemaContract(ctx, database)
}

func startLeasedAppPostgres(ctx context.Context, lease AppIntegrationLeaseDescriptor, opts AppPostgresOptions) (*AppPostgres, func() error, error) {
	templateName := lease.PostgresTemplateName
	if !appIntegrationDatabaseNamePattern.MatchString(templateName) {
		return nil, nil, fmt.Errorf("integration lease template must be a safe PostgreSQL identifier")
	}
	databaseName, err := newAppIntegrationDatabaseName()
	if err != nil {
		return nil, nil, err
	}
	detachNetwork := func() error { return nil }
	if opts.Network != nil {
		aliases := opts.Aliases
		if len(aliases) == 0 {
			aliases = []string{"postgres"}
		}
		arguments := []string{"network", "connect"}
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if !appIntegrationDatabaseNamePattern.MatchString(alias) {
				return nil, nil, fmt.Errorf("integration PostgreSQL network alias %q is invalid", alias)
			}
			arguments = append(arguments, "--alias", alias)
		}
		arguments = append(arguments, opts.Network.ID, lease.PostgresContainerID)
		if err := runAppIntegrationDockerCommand(ctx, arguments...); err != nil {
			return nil, nil, fmt.Errorf("attach suite PostgreSQL to package network: %w", err)
		}
		detachNetwork = func() error {
			return runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
				return runAppIntegrationDockerCommand(
					cleanupCtx,
					"network", "disconnect", "--force", opts.Network.ID, lease.PostgresContainerID,
				)
			})
		}
	}
	failBeforeDatabase := func(err error) (*AppPostgres, func() error, error) {
		return nil, nil, errors.Join(err, detachNetwork())
	}

	admin, err := sql.Open("postgres", lease.PostgresAdminDSN)
	if err != nil {
		return failBeforeDatabase(fmt.Errorf("open integration database admin connection: %w", err))
	}
	if err := admin.PingContext(ctx); err != nil {
		return failBeforeDatabase(errors.Join(
			fmt.Errorf("ping integration database admin connection: %w", err),
			admin.Close(),
		))
	}
	createSQL := fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s",
		pq.QuoteIdentifier(databaseName),
		pq.QuoteIdentifier(templateName),
	)
	if _, err := admin.ExecContext(ctx, createSQL); err != nil {
		return failBeforeDatabase(errors.Join(
			fmt.Errorf("create integration database lease: %w", err),
			admin.Close(),
		))
	}

	dropLease := func() error {
		dropSQL := fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", pq.QuoteIdentifier(databaseName))
		dropErr := runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
			_, err := admin.ExecContext(cleanupCtx, dropSQL)
			return err
		})
		closeErr := admin.Close()
		return errors.Join(dropErr, closeErr)
	}
	leaseDSN, err := appIntegrationDatabaseDSN(lease.PostgresAdminDSN, databaseName)
	if err != nil {
		return nil, nil, errors.Join(err, dropLease(), detachNetwork())
	}
	sqlDB, err := sql.Open("pgx", leaseDSN)
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open integration database lease: %w", err),
			dropLease(),
			detachNetwork(),
		)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = errors.Join(sqlDB.Close(), dropLease(), detachNetwork())
		})
		return cleanupErr
	}
	if err := waitForAppPostgres(ctx, sqlDB); err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	if err := rebuildLeasedPGroongaIndexes(ctx, sqlDB); err != nil {
		return nil, nil, errors.Join(err, cleanup())
	}
	if opts.BootstrapKratosStub {
		if err := bootstrapAppIntegrationSchemas(sqlDB); err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
	}
	if opts.ApplyAppSchemaSQL {
		if err := requireAppIntegrationSchemaContract(ctx, sqlDB); err != nil {
			return nil, nil, errors.Join(err, cleanup())
		}
	}

	database, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open integration database lease with gorm: %w", err),
			cleanup(),
		)
	}
	return &AppPostgres{
		ContainerID:  lease.PostgresContainerID,
		DatabaseName: databaseName,
		DSN:          leaseDSN,
		SQLDB:        sqlDB,
		DB:           database,
	}, cleanup, nil
}

func (postgres *AppPostgres) InternalDSN(host string) (string, error) {
	if postgres == nil || strings.TrimSpace(postgres.DSN) == "" {
		return "", fmt.Errorf("integration PostgreSQL lease is unavailable")
	}
	parsed, err := url.Parse(postgres.DSN)
	if err != nil {
		return "", fmt.Errorf("parse integration PostgreSQL lease DSN: %w", err)
	}
	host = strings.TrimSpace(host)
	if !appIntegrationDatabaseNamePattern.MatchString(host) {
		return "", fmt.Errorf("integration PostgreSQL internal host %q is invalid", host)
	}
	parsed.Host = host + ":5432"
	return parsed.String(), nil
}

func runAppIntegrationDockerCommand(ctx context.Context, arguments ...string) error {
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func appIntegrationCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), appIntegrationCleanupTimeout)
}

func runAppIntegrationCleanup(cleanup func(context.Context) error) error {
	ctx, cancel := appIntegrationCleanupContext()
	defer cancel()
	return cleanup(ctx)
}

func rebuildLeasedPGroongaIndexes(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `
		SELECT namespace.nspname, relation.relname
		FROM pg_index AS pgindex
		JOIN pg_class AS relation ON relation.oid = pgindex.indexrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		JOIN pg_am AS access_method ON access_method.oid = relation.relam
		WHERE access_method.amname = 'pgroonga'
		ORDER BY namespace.nspname, relation.relname
	`)
	if err != nil {
		return fmt.Errorf("list leased PGroonga indexes: %w", err)
	}
	type indexName struct {
		schema string
		name   string
	}
	var indexes []indexName
	for rows.Next() {
		var index indexName
		if err := rows.Scan(&index.schema, &index.name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan leased PGroonga index: %w", err)
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate leased PGroonga indexes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close leased PGroonga index rows: %w", err)
	}
	for _, index := range indexes {
		statement := fmt.Sprintf(
			"REINDEX INDEX %s.%s",
			pq.QuoteIdentifier(index.schema),
			pq.QuoteIdentifier(index.name),
		)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild leased PGroonga index %s.%s: %w", index.schema, index.name, err)
		}
	}
	return nil
}

func newAppIntegrationDatabaseName() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate integration database lease name: %w", err)
	}
	return "geul_it_" + hex.EncodeToString(random), nil
}

func appIntegrationDatabaseDSN(adminDSN, databaseName string) (string, error) {
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		return "", fmt.Errorf("parse integration database admin DSN: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("integration database admin DSN must use postgres scheme")
	}
	parsed.Path = "/" + databaseName
	return parsed.String(), nil
}

func loadAppIntegrationLease(path string) (AppIntegrationLeaseDescriptor, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("-geul-integration-lease-file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("stat integration lease descriptor: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease descriptor must be a regular file with mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("open integration lease descriptor: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var lease AppIntegrationLeaseDescriptor
	if err := decoder.Decode(&lease); err != nil {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("decode integration lease descriptor: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease descriptor contains trailing JSON")
		}
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("decode integration lease descriptor trailing data: %w", err)
	}
	if lease.Version != AppIntegrationLeaseVersion {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("unsupported integration lease version %d", lease.Version)
	}
	if strings.TrimSpace(lease.PostgresAdminDSN) == "" {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease PostgreSQL admin DSN is required")
	}
	if !appIntegrationContainerIDPattern.MatchString(lease.PostgresContainerID) {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease PostgreSQL container ID is invalid")
	}
	if !appIntegrationDatabaseNamePattern.MatchString(lease.PostgresTemplateName) {
		return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease PostgreSQL template name is invalid")
	}
	if lease.Backend != nil {
		backend := lease.Backend
		if strings.TrimSpace(backend.PostgresDSN) == "" ||
			!appIntegrationDatabaseNamePattern.MatchString(backend.PostgresDatabaseName) ||
			strings.TrimSpace(backend.KratosAdminURL) == "" ||
			strings.TrimSpace(backend.KratosPublicURL) == "" ||
			strings.TrimSpace(backend.OathkeeperAdminURL) == "" ||
			strings.TrimSpace(backend.OathkeeperProxyURL) == "" ||
			strings.TrimSpace(backend.SpiceDBEndpoint) == "" ||
			strings.TrimSpace(backend.SpiceDBToken) == "" ||
			strings.TrimSpace(backend.S3Endpoint) == "" ||
			strings.TrimSpace(backend.HookControlURL) == "" ||
			strings.TrimSpace(backend.HookControlToken) == "" {
			return AppIntegrationLeaseDescriptor{}, fmt.Errorf("integration lease backend resource fields are incomplete")
		}
	}
	return lease, nil
}

func waitForAppPostgres(ctx context.Context, sqlDB *sql.DB) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := sqlDB.PingContext(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func applyAppIntegrationPostgresInitAssets(sqlDB *sql.DB, assetRoot string) error {
	scriptPath, ok, err := appIntegrationPostgresInitScriptFile(assetRoot)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	extensionsSQL, err := loadAppInitHereDocSQL(scriptPath)
	if err != nil {
		return err
	}
	_, err = sqlDB.Exec(extensionsSQL)
	return err
}

func bootstrapAppIntegrationSchemas(sqlDB *sql.DB) error {
	_, err := sqlDB.Exec(`
		CREATE SCHEMA IF NOT EXISTS kratos;
			CREATE TABLE IF NOT EXISTS kratos.identities (
				id UUID PRIMARY KEY,
				external_id TEXT NULL,
				schema_id TEXT NOT NULL DEFAULT 'user',
				traits JSONB NOT NULL DEFAULT '{}'::jsonb,
				metadata_public JSONB DEFAULT '{}'::jsonb,
				metadata_admin JSONB DEFAULT '{}'::jsonb,
				state TEXT NOT NULL DEFAULT 'active',
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW()
			);
			CREATE TABLE IF NOT EXISTS kratos.identity_verifiable_addresses (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				status TEXT NOT NULL DEFAULT 'completed',
				via TEXT NOT NULL DEFAULT 'email',
				verified BOOLEAN NOT NULL DEFAULT FALSE,
				value TEXT NOT NULL,
				verified_at TIMESTAMP NULL,
				identity_id UUID NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
				nid UUID NULL
			);
			CREATE TABLE IF NOT EXISTS kratos.sessions (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				identity_id UUID NOT NULL,
				active BOOLEAN NOT NULL DEFAULT TRUE,
				authenticated_at TIMESTAMP NOT NULL DEFAULT NOW(),
				expires_at TIMESTAMP NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW()
			);
		`)
	return err
}

func applyAppIntegrationSchemaSQL(sqlDB *sql.DB) error {
	return requireAppIntegrationSchemaContract(context.Background(), sqlDB)
}

func applyAppIntegrationSchemaSQLFromRoot(sqlDB *sql.DB, schemaRoot string) error {
	schemaFiles, err := appIntegrationSchemaSQLFiles(schemaRoot)
	if err != nil {
		return err
	}

	for _, schemaFile := range schemaFiles {
		schemaSQL, err := os.ReadFile(schemaFile)
		if err != nil {
			return fmt.Errorf("read current schema SQL %s: %w", schemaFile, err)
		}
		if _, err := sqlDB.Exec(string(schemaSQL)); err != nil {
			return fmt.Errorf("apply current schema SQL %s: %w", schemaFile, err)
		}
	}
	return requireAppIntegrationSchemaRelations(context.Background(), sqlDB)
}

func appIntegrationPostgresInitScriptFile(root string) (string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false, nil
	}

	assetRoot := appIntegrationAssetRootPath(root)
	candidates := []string{
		filepath.Join(assetRoot, appIntegrationPostgresInitScriptRelativePath),
		filepath.Join(assetRoot, filepath.Base(appIntegrationPostgresInitScriptRelativePath)),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, err
		}
	}

	return candidates[0], true, nil
}

func appIntegrationSchemaSQLFiles(root string) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("schema root must point to a reviewed schema checkout")
	}

	schemaFile := filepath.Join(appIntegrationAssetRootPath(root), filepath.FromSlash(appIntegrationSchemaSQLRelativePath))
	info, err := os.Stat(schemaFile)
	if err != nil {
		return nil, fmt.Errorf("validate current schema SQL file %s: %w", schemaFile, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("current schema SQL path is not a regular file: %s", schemaFile)
	}
	return []string{schemaFile}, nil
}

func requireAppIntegrationSchemaRelations(ctx context.Context, sqlDB *sql.DB) error {
	for _, relation := range appIntegrationRequiredSchemaRelations {
		if err := appIntegrationRelationExists(ctx, sqlDB, relation); err != nil {
			return fmt.Errorf("verify app schema relation %s: %w", relation, err)
		}
	}
	return nil
}

func requireAppIntegrationSchemaContract(ctx context.Context, sqlDB *sql.DB) error {
	if err := requireAppIntegrationSchemaRelations(ctx, sqlDB); err != nil {
		return err
	}
	for _, column := range appIntegrationRequiredSchemaColumns {
		exists, err := appIntegrationColumnExists(ctx, sqlDB, column)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf(
				"expected column public.%s.%s to exist in current schema",
				column.table,
				column.column,
			)
		}
	}
	for _, relation := range appIntegrationForbiddenSchemaRelations {
		exists, err := appIntegrationRelationPresent(ctx, sqlDB, relation)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("expected retired relation %s to be absent from current schema", relation)
		}
	}
	for _, column := range appIntegrationForbiddenSchemaColumns {
		exists, err := appIntegrationColumnExists(ctx, sqlDB, column)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf(
				"expected retired column public.%s.%s to be absent from current schema",
				column.table,
				column.column,
			)
		}
	}
	return nil
}

func appIntegrationRelationExists(ctx context.Context, db *sql.DB, relation string) error {
	found, err := appIntegrationRelationPresent(ctx, db, relation)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("expected relation %s to exist in current schema", relation)
	}
	return nil
}

func appIntegrationRelationPresent(ctx context.Context, db *sql.DB, relation string) (bool, error) {
	var found *string
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, relation).Scan(&found); err != nil {
		return false, fmt.Errorf("check relation %s: %w", relation, err)
	}
	return found != nil, nil
}

func appIntegrationColumnExists(
	ctx context.Context,
	db *sql.DB,
	column appIntegrationColumnContract,
) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		)
	`, column.table, column.column).Scan(&exists); err != nil {
		return false, fmt.Errorf(
			"check column public.%s.%s: %w",
			column.table,
			column.column,
			err,
		)
	}
	return exists, nil
}

func loadAppInitHereDocSQL(scriptPath string) (string, error) {
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		return "", err
	}

	script := string(scriptBytes)
	startMarker := "<<'EOSQL'\n"
	start := strings.Index(script, startMarker)
	if start < 0 {
		return "", os.ErrInvalid
	}

	script = script[start+len(startMarker):]
	end := strings.Index(script, "\nEOSQL")
	if end < 0 {
		return "", os.ErrInvalid
	}

	return script[:end], nil
}

func appIntegrationAssetRootPath(root string) string {
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return appIntegrationRepoPath(filepath.Join("..", "..", root))
}

func appIntegrationRepoPath(relativePath string) string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime caller unavailable")
	}
	if suffix, ok := legacyMonorepoRelativePath(relativePath); ok {
		root := appIntegrationModuleRoot(filename)
		if suffix == "" {
			return filepath.Clean(root)
		}
		if splitPath, ok := splitRepoPathForLegacySuffix(root, suffix); ok {
			if _, err := os.Stat(splitPath); err == nil {
				return splitPath
			}
		}
		return filepath.Clean(filepath.Join(root, filepath.FromSlash(suffix)))
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), relativePath))
}

func splitRepoPathForLegacySuffix(moduleRoot, suffix string) (string, bool) {
	mappings := []struct {
		legacy string
		split  string
	}{
		{legacy: "apps/collab", split: "geul-editor-collab"},
		{legacy: "apps/web", split: "geul-web"},
		{legacy: "apps/transcoder", split: "geul-transcoder"},
		{legacy: "infra/kratos", split: "geul-identity/config/kratos"},
		{legacy: "infra/oathkeeper", split: "geul-identity/config/oathkeeper"},
		{legacy: "infra/spicedb", split: "geul-identity/config/spicedb"},
	}
	for _, mapping := range mappings {
		if suffix != mapping.legacy && !strings.HasPrefix(suffix, mapping.legacy+"/") {
			continue
		}
		remainder := strings.TrimPrefix(strings.TrimPrefix(suffix, mapping.legacy), "/")
		fallback := filepath.Clean(filepath.Join(moduleRoot, "..", mapping.split, filepath.FromSlash(remainder)))
		for workspaceRoot := filepath.Dir(moduleRoot); ; workspaceRoot = filepath.Dir(workspaceRoot) {
			candidate := filepath.Clean(filepath.Join(workspaceRoot, mapping.split, filepath.FromSlash(remainder)))
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
			parent := filepath.Dir(workspaceRoot)
			if parent == workspaceRoot {
				break
			}
		}
		return fallback, true
	}
	return "", false
}

func legacyMonorepoRelativePath(relativePath string) (string, bool) {
	const prefix = "../../../../"
	slashPath := filepath.ToSlash(relativePath)
	if slashPath == "../../../.." {
		return "", true
	}
	if strings.HasPrefix(slashPath, prefix) {
		return strings.TrimPrefix(slashPath, prefix), true
	}
	return "", false
}

func appIntegrationModuleRoot(filename string) string {
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
		}
		dir = next
	}
}
