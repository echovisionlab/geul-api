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
	oryTestSessionCookieName = "geul_test_session"
	oryHostLoopback          = "127.0.0.1"
	oryStackSetupTimeout     = 2 * time.Minute
)

// KratosIntegrationImage must carry the Identity settings inventory contract.
// The same source-built image is used by migration and runtime containers.
func KratosIntegrationImage() string {
	if image := strings.TrimSpace(os.Getenv("GEUL_TEST_KRATOS_IMAGE")); image != "" {
		return image
	}
	return "geul-identity-kratos:local"
}

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
	// localKratos stacks keep a private kratos schema the Kratos API never sees.
	localKratos bool
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

// OryIntegrationSuite marks a test binary that runs against the
// orchestrator-owned Ory services. Each test gets its own OryStack from
// PrepareOryIntegrationTest; no state is shared or reset between tests.
type OryIntegrationSuite struct {
	lease     AppIntegrationLeaseDescriptor
	tests     sync.Map // map[*testing.T]*oryIntegrationTestLease
	closeOnce sync.Once
	closeErr  error
}

type oryIntegrationTestLease struct {
	name  string
	stack *OryStack
}

var (
	activeOryIntegrationSuiteMu sync.RWMutex
	activeOryIntegrationSuite   *OryIntegrationSuite
)

// StartOryIntegrationSuite validates the orchestrator lease for one test
// binary. Call Close exactly once from TestMain.
func StartOryIntegrationSuite(context.Context) (*OryIntegrationSuite, error) {
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	if err != nil {
		return nil, err
	}
	if lease.Backend == nil || lease.Backend.PostgresIsolationTemplate == "" {
		return nil, fmt.Errorf("orchestrator-owned Ory integration stack is required")
	}
	suite := &OryIntegrationSuite{lease: lease}
	RegisterIntegrationProcessCleanup("Ory integration suite", suite.Close)
	return suite, nil
}

// connectIsolatedOryStack opens a database and SpiceDB datastore owned by one
// caller on the shared Ory services.
func connectIsolatedOryStack(ctx context.Context, lease AppIntegrationLeaseDescriptor) (*OryStack, []func() error, error) {
	postgres, postgresCleanup, err := openIsolatedAppPostgres(ctx, lease)
	if err != nil {
		return nil, nil, err
	}
	backend := lease.Backend
	spiceDBToken, spiceDBClient, err := openIsolatedSpiceDB(ctx, backend.SpiceDBEndpoint)
	if err != nil {
		return nil, nil, errors.Join(err, postgresCleanup())
	}
	stack := &OryStack{
		DB:              postgres.DB,
		PostgresDSN:     postgres.DSN,
		KratosAdminURL:  backend.KratosAdminURL,
		KratosPublicURL: backend.KratosPublicURL,
		SpiceDBEndpoint: backend.SpiceDBEndpoint,
		SpiceDBToken:    spiceDBToken,
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

// PrepareOryIntegrationTest returns the test's own OryStack: a cloned
// database, a private SpiceDB datastore and the shared Kratos. Subtests share
// their parent's stack. Repeated calls for one test return the same stack.
func PrepareOryIntegrationTest(t *testing.T) *OryStack {
	t.Helper()
	activeOryIntegrationSuiteMu.RLock()
	suite := activeOryIntegrationSuite
	activeOryIntegrationSuiteMu.RUnlock()
	if suite == nil {
		return nil
	}
	return suite.prepareTest(t)
}

func (suite *OryIntegrationSuite) prepareTest(t *testing.T) *OryStack {
	t.Helper()
	if lease, ok := suite.tests.Load(t); ok {
		return lease.(*oryIntegrationTestLease).stack
	}
	if stack := suite.parentTestStack(t.Name()); stack != nil {
		return stack
	}
	stack, cleanups, err := connectIsolatedOryStack(t.Context(), suite.lease)
	if err != nil {
		t.Fatalf("prepare isolated Ory integration stack: %v", err)
	}
	suite.tests.Store(t, &oryIntegrationTestLease{name: t.Name(), stack: stack})
	t.Cleanup(func() {
		suite.tests.Delete(t)
		var cleanupErr error
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanupErr = errors.Join(cleanupErr, cleanups[index]())
		}
		if cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	return stack
}

// PrepareOryIntegrationConcurrentTest documents tests which intentionally open
// additional DB connections. Every stack is isolated, so it is the same call.
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

func (suite *OryIntegrationSuite) Close() error {
	suite.closeOnce.Do(func() {
		suite.closeErr = RunIntegrationSuiteCleanups()
	})
	return suite.closeErr
}

// SetupOryStackWithLocalKratos returns an isolated stack whose kratos schema
// is a private copy (structure and network row) instead of the shared Kratos
// database. Use it for tests that lock or re-read kratos rows inside a
// transaction, such as identity fences: postgres_fdw runs remote transactions
// at REPEATABLE READ, so a concurrent update surfaces as a serialization
// failure instead of the READ COMMITTED wait production sees. The Kratos API
// does not see this copy; CreateUser seeds identities with SQL instead.
func SetupOryStackWithLocalKratos(t *testing.T) *OryStack {
	t.Helper()
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	require.NoError(t, err)
	require.NotNil(t, lease.Backend, "orchestrator-owned Ory integration stack is required")
	postgres, postgresCleanup, err := openLocalKratosAppPostgres(t.Context(), lease)
	if postgresCleanup != nil {
		registerIntegrationCleanup(t, "local Kratos test database", postgresCleanup)
	}
	require.NoError(t, err)
	spiceDBToken, spiceDBClient, err := openIsolatedSpiceDB(t.Context(), lease.Backend.SpiceDBEndpoint)
	require.NoError(t, err)
	registerIntegrationCleanup(t, "local Kratos test SpiceDB client", spiceDBClient.Close)
	stack := &OryStack{
		DB:              postgres.DB,
		PostgresDSN:     postgres.DSN,
		KratosAdminURL:  lease.Backend.KratosAdminURL,
		KratosPublicURL: lease.Backend.KratosPublicURL,
		SpiceDBEndpoint: lease.Backend.SpiceDBEndpoint,
		SpiceDBToken:    spiceDBToken,
		KratosClient:    auth.NewKratosClient(lease.Backend.KratosAdminURL),
		SpiceDBClient:   spiceDBClient,
		postgres:        postgres,
		localKratos:     true,
	}
	// Later PrepareOryIntegrationTest calls in this test (for example from
	// IntegrationAdminContext) return this stack instead of opening another.
	activeOryIntegrationSuiteMu.RLock()
	suite := activeOryIntegrationSuite
	activeOryIntegrationSuiteMu.RUnlock()
	if suite != nil {
		if _, loaded := suite.tests.LoadOrStore(t, &oryIntegrationTestLease{name: t.Name(), stack: stack}); loaded {
			t.Fatal("SetupOryStackWithLocalKratos must run before PrepareOryIntegrationTest in the same test, or helpers would mix two databases")
		}
		t.Cleanup(func() { suite.tests.Delete(t) })
	}
	return stack
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
	stack, cleanups, err := connectIsolatedOryStack(t.Context(), lease)
	registerIntegrationCleanup(t, "isolated Ory test stack", func() error {
		var cleanupErr error
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
		registerIntegrationCleanup(t, "suite hook upstream", func() error {
			return runAppIntegrationCleanup(unregister)
		})
	}
	return stack
}

func (s *OryStack) CreateUser(t *testing.T, role string) *OryUser {
	t.Helper()

	ctx := context.Background()
	user := &OryUser{
		MemberID:  uuid.NewString(),
		SessionID: uuid.NewString(),
		Email:     fmt.Sprintf("%s-%s@example.test", role, uuid.NewString()),
		Role:      role,
	}
	user.Name = fmt.Sprintf("Test %s %s", role, user.MemberID)
	user.ID = user.MemberID
	require.NoError(t, s.DB.Exec(`
		INSERT INTO member (id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?, TRUE, ?, ARRAY[?]::text[])
	`, user.MemberID, user.Name, strings.ToLower(user.Email), strings.ToLower(user.Email)).Error)

	if s.localKratos {
		user.IdentityID = uuid.NewString()
		require.NoError(t, s.DB.Exec(`
			INSERT INTO kratos.identities (
				id, nid, schema_id, traits, external_id, state,
				metadata_public, metadata_admin, created_at, updated_at
			)
			SELECT ?::uuid, networks.id, 'user', jsonb_build_object('email', ?::text), ?, 'active',
				'{}'::jsonb, '{}'::jsonb, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
			FROM kratos.networks AS networks
			LIMIT 1
		`, user.IdentityID, user.Email, user.MemberID).Error)
	} else {
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
	}
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
