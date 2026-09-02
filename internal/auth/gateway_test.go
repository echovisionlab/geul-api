package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireGatewaySessionProjectsPrincipalResolvedFromSession(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := GatewayIdentityFromContext(r.Context())
		require.True(t, ok)
		require.Equal(t, IdentityID(testIdentityID), principal.IdentityID)
		require.Equal(t, MemberID(testMemberID), principal.MemberID)
		require.Equal(t, SessionID(testSessionID), principal.SessionID)
		user := GetUser(r.Context())
		require.NotNil(t, user)
		require.Equal(t, MemberID(testMemberID), user.MemberID)
		require.True(t, user.Authenticated)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := RequireGatewaySession(db, next)
	req := httptest.NewRequest(http.MethodPost, "/upload/part/presign", nil)

	req.Header.Set("X-Session-Id", testSessionID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	require.Equal(t, http.StatusNoContent, response.Code)
	query, args, queryCount := scenario.snapshot()
	require.Equal(t, 1, queryCount)
	require.Contains(t, query, "JOIN kratos.identities")
	require.Contains(t, query, "m.id::text = i.external_id")
	require.Contains(t, query, "i.id = session.identity_id")
	require.Contains(t, query, "i.state = 'active'")
	require.Len(t, args, 1)
	require.Equal(t, testSessionID, args[0].Value)
}

func TestRequireGatewaySessionFailsClosed(t *testing.T) {

	tests := []struct {
		name          string
		sessionID     string
		identityState string
		banned        bool
	}{
		{name: "missing session", identityState: KratosStateActive},
		{name: "uppercase session", sessionID: strings.ToUpper(testSessionID), identityState: KratosStateActive},
		{name: "unknown session", sessionID: testSessionID},
		{name: "inactive identity", sessionID: testSessionID, identityState: KratosStateInactive},
		{name: "banned identity", sessionID: testSessionID, identityState: KratosStateActive, banned: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := newPrincipalTestDB(t, tt.banned, tt.identityState)
			handler := RequireGatewaySession(db, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unauthenticated request reached gateway handler")
			}))
			req := httptest.NewRequest(http.MethodPost, "/upload/part/presign", nil)

			req.Header.Set("X-Session-Id", tt.sessionID)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, req)

			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}

func TestRequireGatewaySessionBlocksUploadBeforeMemberOnboarding(t *testing.T) {
	db, _ := newPrincipalTestDBWithOnboarding(t, false, KratosStateActive, false)
	handler := RequireGatewaySession(db, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("non-onboarded request reached upload handler")
	}))
	req := httptest.NewRequest(http.MethodPost, "/upload/part/presign", nil)

	req.Header.Set("X-Session-Id", testSessionID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	require.Equal(t, http.StatusPreconditionFailed, response.Code)
}

func TestRequireGatewaySessionRejectsInvalidConfiguration(t *testing.T) {
	db, _ := newPrincipalTestDB(t, false, KratosStateActive)

	for _, tc := range []struct {
		name string
		call func()
	}{
		{name: "nil database", call: func() { RequireGatewaySession(nil, http.NotFoundHandler()) }},
		{name: "nil handler", call: func() { RequireGatewaySession(db, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Panics(t, tc.call)
		})
	}
}
