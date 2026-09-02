//go:build integration

package testutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeHarnessTrustEnvironmentMatchesProductionKeys(t *testing.T) {
	t.Parallel()

	require.Equal(t, map[string]string{
		"TOKEN_SIGNING_SECRET": IntegrationTokenSigningSecret,
	}, runtimeCollabTrustEnv(IntegrationTokenSigningSecret))
	require.Equal(t, map[string]string{
		"TOKEN_SIGNING_SECRET": IntegrationTokenSigningSecret,
	}, runtimeOathkeeperTrustEnv(IntegrationTokenSigningSecret))
}

func TestRuntimeHarnessAuthHeadersUseCanonicalSession(t *testing.T) {
	t.Parallel()

	header := make(http.Header)
	ApplyAuthHeaders(header, &OryUser{
		IdentityID: "identity-id",
		MemberID:   "member-id",
		SessionID:  "session-id",
		Email:      "john@example.com",
		Name:       "John Doe",
		Role:       "user",
	})

	require.Equal(t, "session-id", header.Get("X-Session-Id"))
}

func TestBackendIntegrationLeaseCarriesTypedRuntimeResources(t *testing.T) {
	t.Parallel()

	stack := &BackendIntegrationStack{
		Postgres:           &AppPostgres{DSN: "postgres://integration"},
		SpiceDBEndpoint:    "127.0.0.1:50051",
		SpiceDBToken:       "integration-spicedb-token",
		TokenSigningSecret: IntegrationTokenSigningSecret,
		MediaSigningSecret: IntegrationMediaSigningSecret,
	}
	lease := stack.Lease()

	require.Equal(t, "postgres://integration", lease.PostgresDSN)
	require.Equal(t, "127.0.0.1:50051", lease.SpiceDBEndpoint)
	require.Equal(t, "integration-spicedb-token", lease.SpiceDBToken)
}

func TestRuntimeStackUsesOnlySharedBackendS3Lease(t *testing.T) {
	t.Parallel()

	backend := runtimeBackendLeaseFixture()
	stack := newRuntimeStackFromBackendLease(
		&OryStack{},
		backend,
		"http://127.0.0.1:4100",
		"http://127.0.0.1:4200",
		"http://127.0.0.1:4300",
	)

	require.Equal(t, backend.S3Region, stack.S3Region)
	require.Equal(t, backend.S3Endpoint, stack.S3Endpoint)
	require.Equal(t, backend.S3AccessKeyID, stack.S3AccessKeyID)
	require.Equal(t, backend.S3SecretAccessKey, stack.S3SecretAccessKey)
	require.Equal(t, backend.S3ForcePathStyle, stack.S3ForcePathStyle)
	require.Equal(t, backend.S3MediaBucket, stack.S3MediaBucket)
	require.Equal(t, backend.S3CacheBucket, stack.S3CacheBucket)
	require.Equal(t, "http://127.0.0.1:4100", stack.BackendURL)
	require.Equal(t, "http://127.0.0.1:4200", stack.WebURL)
	require.Equal(t, "http://127.0.0.1:4300", stack.CDNURL)
	require.Equal(t, stack.CDNURL, stack.MediaURL)
}

func TestRuntimeBackendLeaseFailsClosedWithoutSharedS3(t *testing.T) {
	t.Parallel()

	backend, err := runtimeBackendLease(AppIntegrationLeaseDescriptor{})
	require.Nil(t, backend)
	require.ErrorContains(t, err, "orchestrator-owned runtime integration stack")

	for _, test := range []struct {
		name   string
		mutate func(*AppIntegrationBackendLease)
		field  string
	}{
		{name: "region", mutate: func(backend *AppIntegrationBackendLease) { backend.S3Region = "" }, field: "S3 region"},
		{name: "endpoint", mutate: func(backend *AppIntegrationBackendLease) { backend.S3Endpoint = "" }, field: "S3 endpoint"},
		{name: "access key", mutate: func(backend *AppIntegrationBackendLease) { backend.S3AccessKeyID = "" }, field: "S3 access key ID"},
		{name: "secret key", mutate: func(backend *AppIntegrationBackendLease) { backend.S3SecretAccessKey = "" }, field: "S3 secret access key"},
		{name: "media bucket", mutate: func(backend *AppIntegrationBackendLease) { backend.S3MediaBucket = "" }, field: "S3 media bucket"},
		{name: "cache bucket", mutate: func(backend *AppIntegrationBackendLease) { backend.S3CacheBucket = "" }, field: "S3 cache bucket"},
	} {
		t.Run(test.name, func(t *testing.T) {
			leaseBackend := runtimeBackendLeaseFixture()
			test.mutate(leaseBackend)
			backend, err := runtimeBackendLease(AppIntegrationLeaseDescriptor{Backend: leaseBackend})
			require.Nil(t, backend)
			require.ErrorContains(t, err, test.field)
		})
	}
}

func TestRuntimeHookCleanupDoesNotClearNewerRegistration(t *testing.T) {
	t.Parallel()

	const controlToken = "runtime-hook-control-token"
	var controlMu sync.Mutex
	var revision uint64
	var activeUpstream string
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+controlToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		controlMu.Lock()
		defer controlMu.Unlock()
		switch request.Method {
		case http.MethodPut:
			var payload struct {
				UpstreamBaseURL string `json:"upstream_base_url"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}
			revision++
			activeUpstream = payload.UpstreamBaseURL
			w.Header().Set("Integration-Hook-Registration", strconv.FormatUint(revision, 10))
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if request.Header.Get("Integration-Hook-Registration") == strconv.FormatUint(revision, 10) {
				activeUpstream = ""
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(control.Close)

	backend := runtimeBackendLeaseFixture()
	backend.HookControlURL = control.URL
	backend.HookControlToken = controlToken
	firstCleanup, err := registerRuntimeHook(context.Background(), backend, "http://127.0.0.1:4101")
	require.NoError(t, err)
	secondCleanup, err := registerRuntimeHook(context.Background(), backend, "http://127.0.0.1:4102")
	require.NoError(t, err)

	require.NoError(t, firstCleanup(context.Background()))
	controlMu.Lock()
	upstreamAfterFirstCleanup := activeUpstream
	controlMu.Unlock()
	require.Equal(t, "http://127.0.0.1:4102", upstreamAfterFirstCleanup)

	require.NoError(t, secondCleanup(context.Background()))
	controlMu.Lock()
	upstreamAfterSecondCleanup := activeUpstream
	controlMu.Unlock()
	require.Empty(t, upstreamAfterSecondCleanup)
}

func runtimeBackendLeaseFixture() *AppIntegrationBackendLease {
	return &AppIntegrationBackendLease{
		S3Region:          "us-east-1",
		S3Endpoint:        "http://127.0.0.1:9000",
		S3AccessKeyID:     "integration-access-key",
		S3SecretAccessKey: "integration-secret-key",
		S3MediaBucket:     "integration-media",
		S3CacheBucket:     "integration-cache",
		S3ForcePathStyle:  true,
		HookControlURL:    "http://127.0.0.1:4400/control",
		HookControlToken:  "integration-hook-control-token",
	}
}

func TestOryHarnessUsesCanonicalTokenSigningSecret(t *testing.T) {
	t.Parallel()

	env := oryKratosEnv(
		t,
		"postgres://integration",
		"http://127.0.0.1:3000",
		"http://127.0.0.1:8000",
	)

	require.Equal(t, IntegrationTokenSigningSecret, env["TOKEN_SIGNING_SECRET"])
	require.NotContains(t, env, "SESSION_COOKIE_DOMAIN")
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
		require.Equal(t, IntegrationTokenSigningSecret, env[prefix+"_AUTH_CONFIG_VALUE"], prefix)
		require.NotContains(t, env, prefix+"_HEADERS", prefix)
	}
}
