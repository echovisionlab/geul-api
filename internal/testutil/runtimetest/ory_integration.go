//go:build integration

package runtimetest

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	orytest "github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
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
		"HYDRA_ADMIN_URL":                        "http://hydra:4445",
		"KRATOS_PASSKEY_RP_ID":                   "127.0.0.1",
		"TOKEN_SIGNING_SECRET":                   IntegrationTokenSigningSecret,
		"INTERNAL_SERVICE_HEADER_NAME":           "X-Internal-Service",
		"SECRETS_COOKIE_0":                       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"SECRETS_CIPHER_0":                       "0123456789abcdef0123456789abcdef",
		"CIPHERS_ALGORITHM":                      "xchacha20-poly1305",
		"SERVE_PUBLIC_BASE_URL":                  "http://kratos:4433",
		"SERVE_PUBLIC_CORS_ALLOWED_ORIGINS_0":    browserOrigin,
		"SITE_ORIGIN":                            browserOrigin,
		"SESSION_COOKIE_NAME":                    orytest.OryTestSessionCookieName,
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
