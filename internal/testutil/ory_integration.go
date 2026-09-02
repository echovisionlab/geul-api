//go:build integration

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	oryKratosImage           = "oryd/kratos:v26.2.0@sha256:2a13bb8d362c7a7ae33bd7c0f5168aee46921f15c916a06346db91c06dc76643"
	oryTestSessionCookieName = "geul_test_session"
	oryHostLoopback          = "127.0.0.1"
	oryStackSetupTimeout     = 2 * time.Minute
)

func hostAccessOptionsForURL(rawURL string) ([]testcontainers.ContainerCustomizer, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if parsed.Hostname() != testcontainers.HostInternal {
		return nil, nil
	}
	port := parsed.Port()
	if port == "" {
		return nil, fmt.Errorf("host access URL %q must include a port", rawURL)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("parse host access port %q: %w", port, err)
	}
	return []testcontainers.ContainerCustomizer{testcontainers.WithHostPortAccess(portNumber)}, nil
}

type OryStack struct {
	DB              *gorm.DB
	PostgresDSN     string
	KratosAdminURL  string
	KratosPublicURL string
	SpiceDBEndpoint string
	SpiceDBToken    string
	KratosClient    *auth.KratosClient
	SpiceDBClient   *auth.SpiceDBClient
	postgres        *AppPostgres
}

type OryUser struct {
	// ID is a test-only compatibility alias for MemberID. IdentityID is always
	// distinct and must be used for Kratos operations.
	ID         string
	IdentityID string
	MemberID   string
	SessionID  string
	Email      string
	Name       string
	Role       string
}

func (user *OryUser) AuthUserInfo() *auth.UserInfo {
	if user == nil {
		return nil
	}
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(user.IdentityID),
		MemberID:      auth.MemberID(user.MemberID),
		SessionID:     auth.SessionID(user.SessionID),
		Authenticated: true,
		Onboarded:     true,
	}
}

func ApplyAuthHeaders(header http.Header, user *OryUser) {
	if user == nil {
		return
	}
	header.Set("X-Session-Id", user.SessionID)
}

func SetupOryStack(t *testing.T) *OryStack {
	return SetupOryStackWithOptions(t, OryStackOptions{})
}

type OryStackOptions struct {
	BrowserBaseURL string
	HookBaseURL    string
	IgnoreExternal bool
}

// OryIntegrationSuite borrows the orchestrator-owned PostgreSQL, Kratos, and
// SpiceDB stack for a Go test binary. PrepareOryIntegrationTest serializes users of that shared
// state because service transactions commit independently across PostgreSQL
// and SpiceDB, while Kratos also writes through its own connection.
type OryIntegrationSuite struct {
	stack    *OryStack
	cleanups []func() error
	baseline *IntegrationDatabaseBaseline

	testMu    sync.Mutex
	tests     sync.Map // map[*testing.T]*oryIntegrationTestLease
	closeOnce sync.Once
	closeErr  error
}

type oryIntegrationTestLease struct {
	name              string
	stack             *OryStack
	kratosIdentityIDs map[string]struct{}
}

// IntegrationDatabaseBaseline is an exact post-schema data snapshot for an
// orchestrator-leased PostgreSQL database. Restore removes committed test state
// without reapplying schema SQL or discarding bootstrap rows.
type IntegrationDatabaseBaseline struct {
	postgres  *AppPostgres
	db        *gorm.DB
	hostPath  string
	closeOnce sync.Once
	closeErr  error
}

var (
	activeOryIntegrationSuiteMu sync.RWMutex
	activeOryIntegrationSuite   *OryIntegrationSuite
)

// StartOryIntegrationSuite borrows the orchestrator-owned Ory stack for one
// test binary. Call Close exactly once from TestMain.
func StartOryIntegrationSuite(ctx context.Context) (*OryIntegrationSuite, error) {
	ctx, cancel := context.WithTimeout(ctx, oryStackSetupTimeout)
	defer cancel()

	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	if err != nil {
		return nil, err
	}
	if lease.Backend == nil {
		return nil, fmt.Errorf("orchestrator-owned Ory integration stack is required")
	}
	suite := &OryIntegrationSuite{}
	stack, cleanups, err := connectSharedOryStack(ctx, lease)
	if err != nil {
		return nil, err
	}
	suite.stack = stack
	suite.cleanups = append(suite.cleanups, cleanups...)
	if err := ResetOryIntegrationState(ctx, suite.stack); err != nil {
		return nil, errors.Join(err, suite.Close())
	}
	baseline, err := CaptureIntegrationDatabaseBaseline(ctx, suite.stack.postgres, suite.stack.DB)
	if err != nil {
		return nil, errors.Join(err, suite.Close())
	}
	suite.baseline = baseline
	suite.cleanups = append(suite.cleanups, baseline.Close)
	suite.cleanups = append(suite.cleanups, func() error {
		return errors.Join(
			runAppIntegrationCleanup(baseline.Restore),
			runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
				return ResetOryIntegrationState(cleanupCtx, suite.stack)
			}),
		)
	})
	RegisterIntegrationProcessCleanup("Ory integration suite", suite.Close)
	return suite, nil
}

func connectSharedOryStack(ctx context.Context, lease AppIntegrationLeaseDescriptor) (*OryStack, []func() error, error) {
	postgres, postgresCleanup, err := connectSharedAppPostgres(ctx, lease)
	if err != nil {
		return nil, nil, err
	}
	backend := lease.Backend
	spiceDBClient, err := auth.NewSpiceDBClient(backend.SpiceDBEndpoint, backend.SpiceDBToken, true)
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("connect suite SpiceDB: %w", err),
			postgresCleanup(),
		)
	}
	stack := &OryStack{
		DB:              postgres.DB,
		PostgresDSN:     postgres.DSN,
		KratosAdminURL:  backend.KratosAdminURL,
		KratosPublicURL: backend.KratosPublicURL,
		SpiceDBEndpoint: backend.SpiceDBEndpoint,
		SpiceDBToken:    backend.SpiceDBToken,
		KratosClient:    auth.NewKratosClient(backend.KratosAdminURL),
		SpiceDBClient:   spiceDBClient,
		postgres:        postgres,
	}
	return stack, []func() error{postgresCleanup, spiceDBClient.Close}, nil
}

// ActivateOryIntegrationSuite makes suite the stack returned by
// SetupOryStack for this test binary. TestMain must deactivate it after m.Run.
func ActivateOryIntegrationSuite(suite *OryIntegrationSuite) {
	activeOryIntegrationSuiteMu.Lock()
	defer activeOryIntegrationSuiteMu.Unlock()
	activeOryIntegrationSuite = suite
}

func DeactivateOryIntegrationSuite(suite *OryIntegrationSuite) {
	activeOryIntegrationSuiteMu.Lock()
	defer activeOryIntegrationSuiteMu.Unlock()
	if activeOryIntegrationSuite == suite {
		activeOryIntegrationSuite = nil
	}
}

// PrepareOryIntegrationTest holds one suite lease for the whole test. The
// returned DB is the suite's base handle: service-owned transactions commit
// normally, and cleanup restores the captured PostgreSQL, Kratos, PGMQ, and
// SpiceDB baseline before another test can acquire the lease. Tests which call
// t.Parallel must do so before this function (the current callers do).
func PrepareOryIntegrationTest(t *testing.T) *OryStack {
	t.Helper()
	activeOryIntegrationSuiteMu.RLock()
	suite := activeOryIntegrationSuite
	activeOryIntegrationSuiteMu.RUnlock()
	if suite == nil {
		return nil
	}

	if lease, ok := suite.tests.Load(t); ok {
		return lease.(*oryIntegrationTestLease).stack
	}
	if stack := suite.parentTestStack(t.Name()); stack != nil {
		return stack
	}
	suite.testMu.Lock()
	if lease, loaded := suite.tests.Load(t); loaded {
		suite.testMu.Unlock()
		return lease.(*oryIntegrationTestLease).stack
	}
	if stack := suite.parentTestStack(t.Name()); stack != nil {
		suite.testMu.Unlock()
		return stack
	}
	identities, err := integrationKratosIdentityIDs(t.Context(), suite.stack)
	if err != nil {
		suite.testMu.Unlock()
		t.Fatalf("snapshot Kratos identities before test: %v", err)
	}
	lease := &oryIntegrationTestLease{name: t.Name(), stack: suite.stack, kratosIdentityIDs: identities}
	suite.tests.Store(t, lease)
	t.Cleanup(func() {
		suite.tests.Delete(t)
		defer suite.testMu.Unlock()
		cleanupErr := cleanupOryIntegrationTestLease(
			func() error { return runAppIntegrationCleanup(suite.baseline.Restore) },
			func() error {
				return runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
					return resetKratosIdentityDelta(cleanupCtx, suite.stack, lease.kratosIdentityIDs)
				})
			},
			func() error {
				return runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
					return ResetOryIntegrationState(cleanupCtx, suite.stack)
				})
			},
		)
		if cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	return lease.stack
}

func cleanupOryIntegrationTestLease(
	restoreDatabaseBaseline func() error,
	resetKratosIdentityDelta func() error,
	resetExternalState func() error,
) error {
	var cleanupErr error
	if err := restoreDatabaseBaseline(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore PostgreSQL integration baseline after test: %w", err))
	}
	if err := resetKratosIdentityDelta(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("reset Kratos identity delta after test: %w", err))
	}
	if err := resetExternalState(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("reset shared Ory integration state after test: %w", err))
	}
	return cleanupErr
}

// PrepareOryIntegrationConcurrentTest is the same committed lease with a name
// that documents tests which intentionally open additional DB connections.
func PrepareOryIntegrationConcurrentTest(t *testing.T) *OryStack {
	return PrepareOryIntegrationTest(t)
}

func (suite *OryIntegrationSuite) parentTestStack(name string) *OryStack {
	var parent *oryIntegrationTestLease
	suite.tests.Range(func(_, value any) bool {
		lease := value.(*oryIntegrationTestLease)
		if strings.HasPrefix(name, lease.name+"/") && (parent == nil || len(lease.name) > len(parent.name)) {
			parent = lease
		}
		return true
	})
	if parent == nil {
		return nil
	}
	return parent.stack
}

func (suite *OryIntegrationSuite) Stack() *OryStack { return suite.stack }

func (suite *OryIntegrationSuite) Close() error {
	suite.closeOnce.Do(func() {
		suite.closeErr = errors.Join(suite.closeErr, RunIntegrationSuiteCleanups())
		for index := len(suite.cleanups) - 1; index >= 0; index-- {
			if err := suite.cleanups[index](); err != nil {
				suite.closeErr = errors.Join(suite.closeErr, err)
			}
		}
	})
	return suite.closeErr
}

// ResetOryIntegrationState restores shared queue and authorization state after
// PostgreSQL has returned to its captured post-schema baseline.
func ResetOryIntegrationState(ctx context.Context, stack *OryStack) error {
	if stack == nil || stack.DB == nil || stack.SpiceDBClient == nil {
		return fmt.Errorf("ory integration stack is not initialized")
	}
	return resetOryIntegrationExternalState(
		func() error { return purgeIntegrationPGMQ(ctx, stack) },
		func() error {
			return ResetSpiceDBIntegrationState(
				ctx,
				stack.SpiceDBEndpoint,
				stack.SpiceDBToken,
				stack.SpiceDBClient,
			)
		},
	)
}

func resetOryIntegrationExternalState(purgePGMQ func() error, resetSpiceDB func() error) error {
	var resetErr error
	if err := purgePGMQ(); err != nil {
		resetErr = errors.Join(resetErr, fmt.Errorf("purge PGMQ: %w", err))
	}
	if err := resetSpiceDB(); err != nil {
		resetErr = errors.Join(resetErr, fmt.Errorf("reset SpiceDB: %w", err))
	}
	return resetErr
}

// CaptureIntegrationDatabaseBaseline captures a leased database after schema
// and bootstrap setup have completed. The orchestrator container remains the
// only PostgreSQL process owner; this package only executes dump and restore.
func CaptureIntegrationDatabaseBaseline(ctx context.Context, postgres *AppPostgres, db *gorm.DB) (*IntegrationDatabaseBaseline, error) {
	if postgres == nil || !appIntegrationContainerIDPattern.MatchString(postgres.ContainerID) ||
		!appIntegrationDatabaseNamePattern.MatchString(postgres.DatabaseName) || db == nil {
		return nil, fmt.Errorf("captured database baseline requires an orchestrator PostgreSQL lease")
	}
	path, err := os.CreateTemp("", "geul-postgres-integration-baseline-*.sql")
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL baseline file: %w", err)
	}
	remove := func(err error) (*IntegrationDatabaseBaseline, error) {
		_ = path.Close()
		_ = os.Remove(path.Name())
		return nil, err
	}
	baseline := &IntegrationDatabaseBaseline{
		postgres: postgres,
		db:       db,
		hostPath: path.Name(),
	}
	command := exec.CommandContext(
		ctx,
		"docker", "exec", "-e", "PGPASSWORD=test", postgres.ContainerID,
		"pg_dump", "-U", "test", "-d", postgres.DatabaseName,
		"--data-only", "--schema=public", "--schema=kratos", "--schema=pgmq",
		"--no-owner", "--no-privileges",
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	dump, err := command.Output()
	if err != nil {
		return remove(fmt.Errorf("dump PostgreSQL integration baseline: %w: %s", err, strings.TrimSpace(stderr.String())))
	}
	const emptySearchPath = "SELECT pg_catalog.set_config('search_path', '', false);"
	if !bytes.Contains(dump, []byte(emptySearchPath)) {
		return remove(fmt.Errorf("PostgreSQL integration baseline has no pg_dump empty search_path setting"))
	}
	dump = bytes.ReplaceAll(dump, []byte(emptySearchPath), []byte("SET search_path = public, pg_catalog;"))
	if _, err := path.Write(dump); err != nil {
		return remove(fmt.Errorf("write PostgreSQL integration baseline: %w", err))
	}
	if err := path.Close(); err != nil {
		return remove(fmt.Errorf("close PostgreSQL integration baseline: %w", err))
	}
	return baseline, nil
}

// Restore replaces every mutable public, Kratos, and PGMQ table with the exact
// captured data, including schema bootstrap rows and sequence positions.
func (baseline *IntegrationDatabaseBaseline) Restore(ctx context.Context) error {
	if baseline == nil || baseline.postgres == nil ||
		!appIntegrationContainerIDPattern.MatchString(baseline.postgres.ContainerID) ||
		!appIntegrationDatabaseNamePattern.MatchString(baseline.postgres.DatabaseName) || baseline.db == nil {
		return fmt.Errorf("PostgreSQL integration baseline is unavailable")
	}
	tables, err := integrationDatabaseTables(ctx, baseline.db)
	if err != nil {
		return err
	}
	if len(tables) > 0 {
		if err := baseline.db.WithContext(ctx).Exec("TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE").Error; err != nil {
			return fmt.Errorf("truncate PostgreSQL integration state: %w", err)
		}
	}
	file, err := os.Open(baseline.hostPath)
	if err != nil {
		return fmt.Errorf("open PostgreSQL integration baseline: %w", err)
	}
	defer file.Close()
	command := exec.CommandContext(
		ctx,
		"docker", "exec", "-i", "-e", "PGPASSWORD=test", baseline.postgres.ContainerID,
		"psql", "-v", "ON_ERROR_STOP=1", "-U", "test", "-d", baseline.postgres.DatabaseName,
	)
	command.Stdin = file
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	if err != nil {
		return fmt.Errorf("restore PostgreSQL integration baseline: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Close removes the protected host copy.
func (baseline *IntegrationDatabaseBaseline) Close() error {
	if baseline == nil {
		return nil
	}
	baseline.closeOnce.Do(func() {
		baseline.closeErr = os.Remove(baseline.hostPath)
		if errors.Is(baseline.closeErr, os.ErrNotExist) {
			baseline.closeErr = nil
		}
	})
	return baseline.closeErr
}

func integrationDatabaseTables(ctx context.Context, db *gorm.DB) ([]string, error) {
	rows, err := db.WithContext(ctx).Raw(`
		SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname)
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('public', 'kratos', 'pgmq')
		  AND c.relkind = 'r'
		  -- PostGIS owns and preloads this extension reference table; pg_dump
		  -- intentionally excludes it, and integration tests must not reset it.
		  AND NOT (n.nspname = 'public' AND c.relname = 'spatial_ref_sys')
		ORDER BY n.nspname, c.relname
	`).Rows()
	if err != nil {
		return nil, fmt.Errorf("list PostgreSQL integration tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL integration table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL integration tables: %w", err)
	}
	return tables, nil
}

func integrationKratosIdentityIDs(ctx context.Context, stack *OryStack) (map[string]struct{}, error) {
	rows, err := stack.DB.WithContext(ctx).Raw(`SELECT id::text FROM kratos.identities`).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func resetKratosIdentityDelta(ctx context.Context, stack *OryStack, baseline map[string]struct{}) error {
	current, err := integrationKratosIdentityIDs(ctx, stack)
	if err != nil {
		return err
	}
	for id := range current {
		if _, exists := baseline[id]; exists {
			continue
		}
		if err := stack.DB.WithContext(ctx).Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, id).Error; err != nil {
			return err
		}
	}
	return nil
}

func purgeIntegrationPGMQ(ctx context.Context, stack *OryStack) error {
	rows, err := stack.DB.WithContext(ctx).Raw(`SELECT queue_name FROM pgmq.meta ORDER BY queue_name`).Rows()
	if err != nil {
		return fmt.Errorf("list PGMQ queues: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var queueName string
		if err := rows.Scan(&queueName); err != nil {
			return err
		}
		if err := stack.DB.WithContext(ctx).Exec(`SELECT pgmq.purge_queue(?)`, queueName).Error; err != nil {
			return fmt.Errorf("purge PGMQ queue %s: %w", queueName, err)
		}
	}
	return rows.Err()
}

func SetupOryStackWithOptions(t *testing.T, opts OryStackOptions) *OryStack {
	t.Helper()
	if opts.BrowserBaseURL == "" && opts.HookBaseURL == "" && !opts.IgnoreExternal {
		if stack := PrepareOryIntegrationTest(t); stack != nil {
			return stack
		}
	}
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	require.NoError(t, err)
	require.NotNil(t, lease.Backend, "orchestrator-owned Ory integration stack is required")
	stack, cleanups, err := connectSharedOryStack(t.Context(), lease)
	var baseline *IntegrationDatabaseBaseline
	registerIntegrationCleanup(t, "orchestrator Ory test lease", func() error {
		var cleanupErr error
		if baseline != nil {
			cleanupErr = errors.Join(
				runAppIntegrationCleanup(baseline.Restore),
				runAppIntegrationCleanup(func(cleanupCtx context.Context) error {
					return ResetOryIntegrationState(cleanupCtx, stack)
				}),
				baseline.Close(),
			)
		}
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanupErr = errors.Join(cleanupErr, cleanups[index]())
		}
		return cleanupErr
	})
	require.NoError(t, err)
	if strings.TrimSpace(opts.HookBaseURL) != "" {
		unregister, err := registerSuiteHookUpstream(t.Context(), lease.Backend, opts.HookBaseURL)
		if err != nil {
			t.Fatalf("register suite hook upstream: %v", err)
		}
		cleanups = append(cleanups, func() error {
			return runAppIntegrationCleanup(unregister)
		})
	}
	require.NoError(t, ResetOryIntegrationState(t.Context(), stack))
	baseline, err = CaptureIntegrationDatabaseBaseline(t.Context(), stack.postgres, stack.DB)
	require.NoError(t, err)
	return stack
}

func (s *OryStack) CreateUser(t *testing.T, role string) *OryUser {
	t.Helper()

	ctx := context.Background()
	user := &OryUser{
		MemberID:  uuid.NewString(),
		SessionID: uuid.NewString(),
		Email:     fmt.Sprintf("%d-%s@example.test", time.Now().UnixNano(), role),
		Role:      role,
	}
	user.Name = fmt.Sprintf("Test %s %s", role, user.MemberID)
	user.ID = user.MemberID
	require.NoError(t, s.DB.Exec(`
		INSERT INTO member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?, TRUE, ?, ARRAY[?]::text[])
	`, user.MemberID, user.Name, strings.ToLower(user.Email), strings.ToLower(user.Email)).Error)

	payload := structured.Fields{
		"schema_id":   "user",
		"external_id": user.MemberID,
		"traits": structured.Fields{
			"email": user.Email,
		},
		"verifiable_addresses": []structured.Fields{
			{
				"value":    user.Email,
				"via":      "email",
				"verified": true,
				"status":   "completed",
			},
		},
	}

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.KratosAdminURL+"/admin/identities", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("create kratos identity: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var identity auth.Identity
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&identity))
	user.IdentityID = identity.ID
	require.NotEqual(t, user.MemberID, user.IdentityID)
	require.NoError(t, s.DB.Exec(`
		INSERT INTO account_identity (id, created_at)
		SELECT id, created_at
		FROM kratos.identities
		WHERE id = ?::uuid
		ON CONFLICT (id) DO NOTHING
	`, user.IdentityID).Error)
	require.NoError(t, s.DB.Exec(`
		UPDATE member
		SET account_identity_id = ?::uuid, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?::uuid AND account_identity_id IS NULL
	`, user.IdentityID, user.MemberID).Error)
	require.NoError(t, s.DB.Exec(`
		INSERT INTO kratos.sessions (
			id, expires_at, authenticated_at, identity_id,
			created_at, updated_at, active, nid, authentication_methods
		)
		SELECT
			?::uuid, CURRENT_TIMESTAMP + INTERVAL '24 hours', CURRENT_TIMESTAMP, id,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, TRUE, nid, '[]'::jsonb
		FROM kratos.identities
		WHERE id = ?::uuid
	`, user.SessionID, user.IdentityID).Error)

	s.MarkUserEmailVerified(t, user)

	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(user.IdentityID))
	require.NoError(t, err)
	roleID, ok := policyv1.Role.Parse(role)
	require.True(t, ok, "unsupported SpiceDB role %q", role)
	if !ok {
		return user
	}
	_, err = s.SpiceDBClient.SyncAccountIdentityGlobalRole(ctx, subject, roleID)
	require.NoError(t, err)

	return user
}

func (s *OryStack) MarkUserEmailVerified(t *testing.T, user *OryUser) {
	t.Helper()
	require.NotNil(t, user)
	require.NotEmpty(t, user.IdentityID)
	require.NotEmpty(t, user.Email)

	now := time.Now().UTC()
	require.NoError(t, s.DB.Exec(
		`DELETE FROM kratos.identity_verifiable_addresses
		WHERE identity_id = ?::uuid
			AND via = 'email'
			AND lower(value) = lower(?::text)`,
		user.IdentityID,
		user.Email,
	).Error)
	result := s.DB.Exec(
		`INSERT INTO kratos.identity_verifiable_addresses (
			id,
			status,
			via,
			verified,
			value,
			verified_at,
			identity_id,
			created_at,
			updated_at,
			nid
		)
		SELECT
			gen_random_uuid(),
			'completed',
			'email',
			TRUE,
			?,
			?,
			?::uuid,
			?,
			?,
			identities.nid
		FROM kratos.identities AS identities
		WHERE identities.id = ?::uuid`,
		user.Email,
		now,
		user.IdentityID,
		now,
		now,
		user.IdentityID,
	)
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
}

func oryKratosEnv(t *testing.T, dsn string, browserBaseURL string, hookBaseURL string) map[string]string {
	t.Helper()

	browserOrigin := "http://127.0.0.1:3000"
	hookOrigin := "http://127.0.0.1:65535"
	if trimmed := strings.TrimSpace(browserBaseURL); trimmed != "" {
		parsed, err := url.Parse(trimmed)
		require.NoError(t, err)
		require.NotEmpty(t, parsed.Scheme)
		require.NotEmpty(t, parsed.Host)
		browserOrigin = strings.TrimRight(trimmed, "/")
	}
	if trimmed := strings.TrimSpace(hookBaseURL); trimmed != "" {
		parsed, err := url.Parse(trimmed)
		require.NoError(t, err)
		require.NotEmpty(t, parsed.Scheme)
		require.NotEmpty(t, parsed.Host)
		hookOrigin = strings.TrimRight(trimmed, "/")
	}

	environment := map[string]string{
		"DSN":                                    dsn,
		"KRATOS_ADMIN_URL":                       "http://kratos:4434",
		"KRATOS_PASSKEY_RP_ID":                   "127.0.0.1",
		"TOKEN_SIGNING_SECRET":                   IntegrationTokenSigningSecret,
		"INTERNAL_SERVICE_HEADER_NAME":           "X-Internal-Service",
		"SECRETS_COOKIE_0":                       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"SECRETS_CIPHER_0":                       "0123456789abcdef0123456789abcdef",
		"CIPHERS_ALGORITHM":                      "xchacha20-poly1305",
		"SERVE_PUBLIC_BASE_URL":                  "http://kratos:4433",
		"SERVE_PUBLIC_CORS_ALLOWED_ORIGINS_0":    browserOrigin,
		"SITE_ORIGIN":                            browserOrigin,
		"SESSION_COOKIE_NAME":                    oryTestSessionCookieName,
		"SELFSERVICE_DEFAULT_BROWSER_RETURN_URL": browserOrigin,
		"SELFSERVICE_ALLOWED_RETURN_URLS_0":      browserOrigin,
		"SELFSERVICE_ALLOWED_RETURN_URLS_1":      browserOrigin + "/my/security",
		"SELFSERVICE_FLOWS_LOGIN_UI_URL":         browserOrigin + "/login",
		"SELFSERVICE_FLOWS_REGISTRATION_UI_URL":  browserOrigin + "/login",
		"SELFSERVICE_FLOWS_LOGOUT_AFTER_DEFAULT_BROWSER_RETURN_URL":                     browserOrigin,
		"SELFSERVICE_FLOWS_VERIFICATION_UI_URL":                                         browserOrigin + "/verify",
		"SELFSERVICE_FLOWS_VERIFICATION_AFTER_DEFAULT_BROWSER_RETURN_URL":               browserOrigin,
		"SELFSERVICE_FLOWS_SETTINGS_UI_URL":                                             browserOrigin + "/my/settings",
		"SELFSERVICE_FLOWS_ERROR_UI_URL":                                                browserOrigin + "/login/error",
		"COURIER_DELIVERY_STRATEGY":                                                     "http",
		"COURIER_HTTP_REQUEST_CONFIG_URL":                                               hookOrigin + "/api.intra.v1.EmailCourierService/SendEmail",
		"COURIER_HTTP_REQUEST_CONFIG_AUTH_CONFIG_VALUE":                                 IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_LOGIN_AFTER_OIDC_HOOKS_0_CONFIG_URL":                         hookOrigin + "/hooks/after-login",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":           IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_LOGIN_AFTER_CODE_HOOKS_0_CONFIG_URL":                         hookOrigin + "/hooks/after-login",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_CODE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":           IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_LOGIN_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":                      hookOrigin + "/hooks/after-login",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_0_CONFIG_URL":                  hookOrigin + "/hooks/reject-credential-registration",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_1_CONFIG_URL":                  hookOrigin + "/hooks/after-login",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_0_CONFIG_URL":                  hookOrigin + "/hooks/reject-credential-registration",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_1_CONFIG_URL":                  hookOrigin + "/hooks/after-login",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":               hookOrigin + "/hooks/reject-credential-registration",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE": IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_VERIFICATION_AFTER_HOOKS_0_CONFIG_URL":                       hookOrigin + "/hooks/after-verification",
		"SELFSERVICE_FLOWS_VERIFICATION_AFTER_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":         IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_0_CONFIG_URL":                      hookOrigin + "/hooks/pre-settings-oidc",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_1_CONFIG_URL":                      hookOrigin + "/hooks/post-settings-oidc",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":                   hookOrigin + "/hooks/pre-settings-passkey",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_1_CONFIG_URL":                   hookOrigin + "/hooks/post-settings-passkey",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PROFILE_HOOKS_0_CONFIG_URL":                   hookOrigin + "/hooks/after-settings",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PROFILE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
		"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_0_CLIENT_ID":                         "dummy-google-client-id",
		"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_0_CLIENT_SECRET":                     "dummy-google-client-secret",
		"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_1_CLIENT_ID":                         "dummy-github-client-id",
		"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_1_CLIENT_SECRET":                     "dummy-github-client-secret",
	}
	applyKratosConfigOverrides(environment, browserOrigin)
	return environment
}

// applyKratosConfigOverrides supplies the flattened environment contract used
// by the public Identity repository. The mounted kratos.yml is deliberately a
// checked-in template; Kratos applies these keys as configuration overrides
// rather than interpolating shell-style placeholders in YAML values.
func applyKratosConfigOverrides(environment map[string]string, browserOrigin string) {
	environment["SERVE_ADMIN_BASE_URL"] = "http://kratos:4434"
	environment["SELFSERVICE_METHODS_PASSKEY_CONFIG_RP_ID"] = "127.0.0.1"
	environment["SELFSERVICE_METHODS_PASSKEY_CONFIG_RP_ORIGINS_0"] = browserOrigin
	for _, prefix := range []string{
		"COURIER_HTTP_REQUEST_CONFIG",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_OIDC_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_CODE_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_LOGIN_AFTER_PASSKEY_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_1_CONFIG",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_1_CONFIG",
		"SELFSERVICE_FLOWS_REGISTRATION_AFTER_PASSKEY_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_VERIFICATION_AFTER_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_1_CONFIG",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_0_CONFIG",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_1_CONFIG",
		"SELFSERVICE_FLOWS_SETTINGS_AFTER_PROFILE_HOOKS_0_CONFIG",
	} {
		environment[prefix+"_AUTH_CONFIG_NAME"] = "X-Internal-Service"
	}
}
