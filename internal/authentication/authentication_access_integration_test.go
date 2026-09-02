//go:build integration

package authentication

import (
	"net/http"
	"net/http/httptest"
	"testing"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAuthenticationAccessPersistsAuthoritativeSuccessWithoutCredentialData(t *testing.T) {
	db := newAuthenticationIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	sessionID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, metadata_public)
		VALUES (?::uuid, ?, '{"role":"user"}'::jsonb)
	`, identityID, memberID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.account_identity (id)
		VALUES (?::uuid)
	`, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.member (
			id, account_identity_id, nickname, onboarded, primary_email, available_emails
		) VALUES (?::uuid, ?::uuid, 'auth-integration', true, 'member@example.test', ARRAY['member@example.test'])
	`, memberID, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.sessions (id, identity_id, active)
		VALUES (?::uuid, ?::uuid, true)
	`, sessionID, identityID).Error)

	recorder, err := NewAuthenticationAccessRecorder(
		"http://kratos.invalid",
		db,
		apitelemetry.NewDurableWriter(db),
	)
	require.NoError(t, err)
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "ory_kratos_session=sensitive; Path=/; HttpOnly")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{"id":"` + sessionID + `"},"session_token":"sensitive"}`))
	}))

	request := authenticationAccessRequest(
		http.MethodPost,
		"https://auth.example/self-service/login?flow=login-flow",
		`{"method":"code","code":"123456","identifier":"member@example.test"}`,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var stored struct {
		Action               string
		ActorMemberID        string
		RequestID            string
		SourceIP             string
		FlowKind             string
		AuthenticationMethod string
		PrincipalState       string
		Provider             *string
		Reason               *string
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, host(source_ip) AS source_ip,
		       attributes->>'flow_kind' AS flow_kind,
		       attributes->>'authentication_method' AS authentication_method,
		       attributes->>'principal_state' AS principal_state,
		       attributes->>'provider' AS provider,
		       attributes->>'reason' AS reason
		FROM public.security_access
	`).Scan(&stored).Error)
	require.Equal(t, string(sharedtelemetry.SecurityAuthenticationSucceeded), stored.Action)
	require.Equal(t, memberID, stored.ActorMemberID)
	require.NotEmpty(t, stored.RequestID)
	require.Equal(t, "192.0.2.44", stored.SourceIP)
	require.Equal(t, string(sharedtelemetry.AuthenticationFlowLogin), stored.FlowKind)
	require.Equal(t, string(sharedtelemetry.AuthenticationMethodEmailCode), stored.AuthenticationMethod)
	require.Equal(t, string(sharedtelemetry.AuthenticationPrincipalActive), stored.PrincipalState)
	require.Nil(t, stored.Provider)
	require.Nil(t, stored.Reason)

	var forbiddenColumns int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'security_access'
		  AND column_name IN ('email', 'identifier', 'code', 'token', 'cookie', 'raw_error')
	`).Scan(&forbiddenColumns).Error)
	require.Zero(t, forbiddenColumns)
}
