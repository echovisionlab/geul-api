//go:build integration

package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/lib/pq"
)

// Every integration test owns its state: a PostgreSQL database cloned from the
// backend isolation template, a SpiceDB datastore (serve-testing keeps one
// in-memory datastore per bearer token) and, when it needs object storage, its
// own buckets. The containers are shared and ephemeral, so nothing is reset
// between tests. Kratos is the one shared service: the cloned database reaches
// the backend's kratos schema through postgres_fdw, and tests may only assert
// on identities they created.

const backendIsolationKratosServer = "backend_kratos"

// PrepareBackendIsolationTemplate clones the suite schema template and points
// its kratos schema at the backend database that Kratos migrates and writes.
// Clones of the result share Kratos state and nothing else.
func PrepareBackendIsolationTemplate(ctx context.Context, lease AppIntegrationLeaseDescriptor, kratosDatabase string) (string, error) {
	if !appIntegrationDatabaseNamePattern.MatchString(lease.PostgresTemplateName) ||
		!appIntegrationDatabaseNamePattern.MatchString(kratosDatabase) {
		return "", fmt.Errorf("backend isolation template requires safe PostgreSQL identifiers")
	}
	adminURL, err := url.Parse(lease.PostgresAdminDSN)
	if err != nil || adminURL.User == nil {
		return "", fmt.Errorf("backend isolation template requires the suite admin credentials")
	}
	password, _ := adminURL.User.Password()
	templateName, err := newAppIntegrationDatabaseName()
	if err != nil {
		return "", err
	}

	admin, err := sql.Open("postgres", lease.PostgresAdminDSN)
	if err != nil {
		return "", fmt.Errorf("open backend isolation admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if err := execRetryingObjectInUse(ctx, admin, fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s",
		pq.QuoteIdentifier(templateName),
		pq.QuoteIdentifier(lease.PostgresTemplateName),
	)); err != nil {
		return "", fmt.Errorf("create backend isolation template: %w", err)
	}

	templateDSN, err := appIntegrationDatabaseDSN(lease.PostgresAdminDSN, templateName)
	if err != nil {
		return "", err
	}
	template, err := sql.Open("postgres", templateDSN)
	if err != nil {
		return "", fmt.Errorf("open backend isolation template: %w", err)
	}
	// The suite PostgreSQL container serves both databases, so the foreign
	// server is the container's own listener.
	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS postgres_fdw`,
		fmt.Sprintf(
			// Remote estimates let the planner push join keys (identity.id = $1)
			// into the foreign scan, so FOR SHARE joins lock only the joined
			// identities instead of every row another test may be updating.
			`CREATE SERVER %s FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host 'localhost', port '5432', dbname %s, use_remote_estimate 'true')`,
			pq.QuoteIdentifier(backendIsolationKratosServer), pq.QuoteLiteral(kratosDatabase),
		),
		fmt.Sprintf(
			`CREATE USER MAPPING FOR CURRENT_USER SERVER %s OPTIONS (user %s, password %s)`,
			pq.QuoteIdentifier(backendIsolationKratosServer),
			pq.QuoteLiteral(adminURL.User.Username()), pq.QuoteLiteral(password),
		),
		`DROP SCHEMA IF EXISTS kratos CASCADE`,
		`CREATE SCHEMA kratos`,
		fmt.Sprintf(
			`IMPORT FOREIGN SCHEMA kratos FROM SERVER %s INTO kratos OPTIONS (import_default 'true')`,
			pq.QuoteIdentifier(backendIsolationKratosServer),
		),
	}
	for _, statement := range statements {
		if _, err := template.ExecContext(ctx, statement); err != nil {
			return "", errors.Join(fmt.Errorf("configure backend isolation template: %w", err), template.Close())
		}
	}
	// CREATE DATABASE ... TEMPLATE requires that nothing is connected to it.
	if err := template.Close(); err != nil {
		return "", fmt.Errorf("close backend isolation template: %w", err)
	}
	return templateName, LockAppIntegrationTemplate(ctx, lease.PostgresAdminDSN, templateName)
}

// PrepareLocalKratosTemplate clones the suite schema template and copies the
// kratos schema structure and network row from the backend database into it.
// Tests on its clones lock kratos rows exactly as production does; the shared
// Kratos API never sees those rows.
func PrepareLocalKratosTemplate(ctx context.Context, lease AppIntegrationLeaseDescriptor, kratosDatabase string) (string, error) {
	if !appIntegrationDatabaseNamePattern.MatchString(lease.PostgresTemplateName) ||
		!appIntegrationDatabaseNamePattern.MatchString(kratosDatabase) ||
		!appIntegrationContainerIDPattern.MatchString(lease.PostgresContainerID) {
		return "", fmt.Errorf("local Kratos template requires safe PostgreSQL identifiers")
	}
	templateName, err := newAppIntegrationDatabaseName()
	if err != nil {
		return "", err
	}
	admin, err := sql.Open("postgres", lease.PostgresAdminDSN)
	if err != nil {
		return "", fmt.Errorf("open local Kratos template admin connection: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if err := execRetryingObjectInUse(ctx, admin, fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s",
		pq.QuoteIdentifier(templateName),
		pq.QuoteIdentifier(lease.PostgresTemplateName),
	)); err != nil {
		return "", fmt.Errorf("create local Kratos template: %w", err)
	}
	copyKratos := fmt.Sprintf(
		"pg_dump -U test -d %[1]s --schema=kratos --schema-only --no-owner --no-privileges | psql -q -v ON_ERROR_STOP=1 -U test -d %[2]s && "+
			"pg_dump -U test -d %[1]s --data-only --table=kratos.networks --no-owner --no-privileges | psql -q -v ON_ERROR_STOP=1 -U test -d %[2]s",
		kratosDatabase, templateName,
	)
	if err := runAppIntegrationDockerCommand(ctx,
		"exec", "-e", "PGPASSWORD=test", lease.PostgresContainerID, "sh", "-c", copyKratos,
	); err != nil {
		return "", fmt.Errorf("copy kratos schema into local Kratos template: %w", err)
	}
	return templateName, LockAppIntegrationTemplate(ctx, lease.PostgresAdminDSN, templateName)
}

// OpenIsolatedAppPostgres clones the backend isolation template for one test.
func OpenIsolatedAppPostgres(ctx context.Context, lease AppIntegrationLeaseDescriptor) (*AppPostgres, func() error, error) {
	if lease.Backend == nil || lease.Backend.PostgresIsolationTemplate == "" {
		return nil, nil, fmt.Errorf("suite backend lease with an isolation template is required")
	}
	isolated := lease
	isolated.PostgresTemplateName = lease.Backend.PostgresIsolationTemplate
	return startLeasedAppPostgres(ctx, isolated, AppPostgresOptions{})
}

// OpenLocalKratosAppPostgres clones the local Kratos template for one test.
func OpenLocalKratosAppPostgres(ctx context.Context, lease AppIntegrationLeaseDescriptor) (*AppPostgres, func() error, error) {
	if lease.Backend == nil || lease.Backend.PostgresLocalKratosTemplate == "" {
		return nil, nil, fmt.Errorf("suite backend lease with a local Kratos template is required")
	}
	isolated := lease
	isolated.PostgresTemplateName = lease.Backend.PostgresLocalKratosTemplate
	return startLeasedAppPostgres(ctx, isolated, AppPostgresOptions{})
}

func NewIsolationIdentifier(prefix string) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate isolation identifier: %w", err)
	}
	return prefix + hex.EncodeToString(random), nil
}
