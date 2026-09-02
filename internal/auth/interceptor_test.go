package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestAuthInterceptorResolvesAssertedSessionPrincipalInOneQuery(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	interceptor := NewAuthInterceptor("http://127.0.0.1:1", db)
	var seenUser *UserInfo
	wrapper := interceptor.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seenUser = GetUser(ctx)
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("X-Real-IP", "127.0.0.1")

	req.Header().Set("X-Session-Id", testSessionID)

	_, err := wrapper(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, seenUser)
	require.Equal(t, IdentityID(testIdentityID), seenUser.IdentityID)
	require.Equal(t, MemberID(testMemberID), seenUser.MemberID)
	require.Equal(t, SessionID(testSessionID), seenUser.SessionID)
	require.True(t, seenUser.Authenticated)
	require.True(t, seenUser.Onboarded)
	query, args, queryCount := scenario.snapshot()
	require.Equal(t, 1, queryCount, "asserted authentication must use one database query")
	require.Contains(t, query, "FROM kratos.sessions AS session")
	require.Contains(t, query, "i.id = session.identity_id")
	require.Contains(t, query, "m.account_identity_id = i.id")
	require.Contains(t, query, "m.id::text = i.external_id")
	require.Contains(t, query, "i.state = 'active'")
	require.Len(t, args, 1)
	require.Equal(t, testSessionID, args[0].Value)
}

func TestAuthInterceptorRejectsUnlinkedSession(t *testing.T) {
	db, _ := newPrincipalTestDB(t, false, "")
	interceptor := NewAuthInterceptor("http://127.0.0.1:1", db)
	wrapper := interceptor.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("unlinked request reached handler")
		return nil, nil
	})
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("X-Real-IP", "127.0.0.1")
	req.Header().Set("X-Session-Id", testSessionID)

	_, err := wrapper(t.Context(), req)
	require.Error(t, err)
}

func TestAuthInterceptorAllowsOnlyAssertedAnonymousPublicRequests(t *testing.T) {
	tests := []struct {
		name       string
		procedure  string
		wantCalled bool
		wantError  bool
	}{
		{
			name:       "public anonymous",
			procedure:  "/api.open.v1.TestService/Get",
			wantCalled: true,
		},
		{
			name:      "private without session",
			procedure: "/api.manage.v1.TestService/Get",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
			interceptor := NewAuthInterceptor("http://127.0.0.1:1", db)
			called := false
			handler := connect.NewUnaryHandler(
				tt.procedure,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					called = true
					return connect.NewResponse(&emptypb.Empty{}), nil
				},
				connect.WithInterceptors(interceptor),
			)
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+tt.procedure)
			req := connect.NewRequest(&emptypb.Empty{})

			_, err := client.CallUnary(t.Context(), req)

			require.Equal(t, tt.wantError, err != nil)
			require.Equal(t, tt.wantCalled, called)
			_, _, queryCount := scenario.snapshot()
			require.Zero(t, queryCount, "anonymous and rejected requests must not query principal state")
		})
	}
}

func TestAuthInterceptorAllowsOnlyExactOnboardingProceduresUntilComplete(t *testing.T) {
	tests := []struct {
		procedure     string
		allowed       bool
		wantPrincipal bool
	}{
		{procedure: "/api.manage.v1.MemberService/GetCurrentSession", allowed: true, wantPrincipal: true},
		{procedure: "/api.manage.v1.MemberService/CheckNicknameAvailability", allowed: true, wantPrincipal: true},
		{procedure: "/api.manage.v1.MemberService/CompleteMyOnboarding", allowed: true, wantPrincipal: true},
		{procedure: "/api.manage.v1.MemberService/UpdateMyProfile", allowed: false},
		{procedure: "/api.open.v1.ManifestService/Get", allowed: true},
		{procedure: "/api.open.v1.PageService/Get", allowed: true},
	}

	for _, tt := range tests {
		t.Run(tt.procedure, func(t *testing.T) {
			db, _ := newPrincipalTestDBWithOnboarding(t, false, KratosStateActive, false)
			interceptor := NewAuthInterceptor("http://127.0.0.1:1", db)
			called := false
			handler := connect.NewUnaryHandler(
				tt.procedure,
				func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					called = true
					if tt.wantPrincipal {
						require.NotNil(t, GetUser(ctx))
						require.False(t, GetUser(ctx).Onboarded)
					} else {
						require.Nil(t, GetUser(ctx), "unfinished Member must be anonymous on public procedures")
					}
					return connect.NewResponse(&emptypb.Empty{}), nil
				},
				connect.WithInterceptors(interceptor),
			)
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+tt.procedure)
			req := connect.NewRequest(&emptypb.Empty{})

			req.Header().Set("X-Session-Id", testSessionID)

			_, err := client.CallUnary(t.Context(), req)

			require.Equal(t, tt.allowed, err == nil)
			require.Equal(t, tt.allowed, called)
			if !tt.allowed {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			}
		})
	}
}

func TestLookupAuthenticatedPrincipalRequiresCanonicalSessionUUID(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)

	tests := []struct {
		name      string
		sessionID string
	}{
		{name: "missing", sessionID: ""},
		{name: "uppercase", sessionID: strings.ToUpper(testSessionID)},
		{name: "whitespace", sessionID: " " + testSessionID},
		{name: "nil UUID", sessionID: "00000000-0000-0000-0000-000000000000"},
		{name: "version zero", sessionID: "cccccccc-cccc-0ccc-accc-cccccccccccc"},
		{name: "wrong variant", sessionID: "cccccccc-cccc-4ccc-7ccc-cccccccccccc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			principal, err := ResolveAuthenticatedPrincipalBySessionID(t.Context(), db, tt.sessionID)
			require.Error(t, err)
			require.Nil(t, principal)
		})
	}
	_, _, queryCount := scenario.snapshot()
	require.Zero(t, queryCount, "non-canonical session input must be rejected before database access")
}

func TestLockAuthenticatedSessionForPrincipalLocksExactActiveSession(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	expected := &UserInfo{
		SessionID: testSessionID, IdentityID: testIdentityID, MemberID: testMemberID,
		Authenticated: true,
	}

	err := LockAuthenticatedSessionForPrincipal(t.Context(), db, testSessionID, expected)

	require.NoError(t, err)
	query, args, queryCount := scenario.snapshot()
	require.Equal(t, 1, queryCount)
	require.Contains(t, query, "session.id = $1::uuid")
	require.Contains(t, query, "session.active = TRUE")
	require.Contains(t, query, "session.expires_at > NOW()")
	require.Contains(t, query, "i.id = session.identity_id")
	require.Contains(t, query, "FOR SHARE OF session")
	require.Len(t, args, 1)
	require.Equal(t, testSessionID, args[0].Value)
}

func TestLockAuthenticatedSessionForPrincipalRejectsChangedPrincipal(t *testing.T) {
	db, _ := newPrincipalTestDB(t, false, KratosStateActive)
	expected := &UserInfo{
		SessionID: testSessionID, IdentityID: testIdentityID,
		MemberID: MemberID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"), Authenticated: true,
	}

	err := LockAuthenticatedSessionForPrincipal(t.Context(), db, testSessionID, expected)

	require.ErrorIs(t, err, ErrSessionPrincipalInvalid)
}

func TestResolveUserUsesWhoamiSessionIDAndDatabaseAuthority(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		require.Equal(t, "/sessions/whoami", r.URL.Path)
		require.Equal(t, "ory_session=api-session-token", r.Header.Get("Cookie"))
		if requestCount > 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(structured.Fields{
			"id":     testSessionID,
			"active": true,
			"identity": structured.Fields{
				// Direct authentication consumes only the whoami session ID. These
				// values deliberately disagree with the database projection.
				"id":          "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
				"external_id": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
				"state":       KratosStateActive,
				// Kratos metadata is not an authorization source.
				"metadata_public": structured.Fields{},
			},
		}))
	}))
	t.Cleanup(server.Close)
	interceptor := &AuthInterceptor{
		kratosPublicURL: server.URL,
		httpClient:      server.Client(),
		db:              db,
		cache:           map[sessionCacheKey]cacheEntry{},
	}

	user, err := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=api-session-token")

	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, IdentityID(testIdentityID), user.IdentityID)
	require.Equal(t, MemberID(testMemberID), user.MemberID)
	require.Equal(t, SessionID(testSessionID), user.SessionID)
	query, args, queryCount := scenario.snapshot()
	require.Equal(t, 1, queryCount)
	require.Contains(t, query, "FROM kratos.sessions AS session")
	require.Len(t, args, 1)
	require.Equal(t, testSessionID, args[0].Value)

	revoked, err := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=api-session-token")
	require.Error(t, err)
	require.Nil(t, revoked)
	require.Equal(t, 2, requestCount, "positive sessions must be revalidated after revocation")
}

func TestResolveUserDoesNotNegativeCachePrincipalDatabaseFailure(t *testing.T) {
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	scenario.queryErr = errors.New("database unavailable")
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(structured.Fields{
			"id":     testSessionID,
			"active": true,
		}))
	}))
	t.Cleanup(server.Close)
	interceptor := &AuthInterceptor{
		kratosPublicURL: server.URL,
		httpClient:      server.Client(),
		db:              db,
		cache:           map[sessionCacheKey]cacheEntry{},
	}

	_, firstErr := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=api-session-token")
	_, secondErr := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=api-session-token")
	require.Error(t, firstErr)
	require.Error(t, secondErr)

	_, _, queryCount := scenario.snapshot()
	require.Equal(t, 2, requestCount)
	require.Equal(t, 2, queryCount, "transient database failures must remain retryable")
}

func TestAuthInterceptorStopsCleanupWithContext(t *testing.T) {
	db, _ := newPrincipalTestDB(t, false, KratosStateActive)
	interceptor := NewAuthInterceptor("http://127.0.0.1:1", db)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		interceptor.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cache cleanup did not stop after context cancellation")
	}
}

func TestAuthInterceptorRemovesOnlyExpiredCacheEntries(t *testing.T) {
	now := time.Now()
	expiredKey := sessionCredentialCacheKey("expired")
	currentKey := sessionCredentialCacheKey("current")
	activeKey := sessionCredentialCacheKey("active")
	interceptor := &AuthInterceptor{cache: map[sessionCacheKey]cacheEntry{
		expiredKey: {expiresAt: now.Add(-time.Second)},
		currentKey: {expiresAt: now},
		activeKey:  {expiresAt: now.Add(time.Second)},
	}}

	interceptor.removeExpiredCacheEntries(now)

	_, expiredExists := interceptor.cache[expiredKey]
	_, currentExists := interceptor.cache[currentKey]
	_, activeExists := interceptor.cache[activeKey]
	require.False(t, expiredExists)
	require.False(t, currentExists)
	require.True(t, activeExists)
}

func TestResolveUserNegativeCachesInvalidSession(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		require.Equal(t, "ory_session=bad", r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	interceptor := &AuthInterceptor{
		kratosPublicURL: server.URL,
		httpClient:      server.Client(),
		cache:           map[sessionCacheKey]cacheEntry{},
	}

	_, firstErr := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=bad")
	_, secondErr := interceptor.resolveUserWithError(t.Context(), "cookie:ory_session=bad")
	require.Error(t, firstErr)
	require.Error(t, secondErr)
	require.Equal(t, 1, requestCount, "second resolve should use the negative session cache")
}

func TestNegativeSessionCacheUsesDigestKeysAndStaysBounded(t *testing.T) {
	interceptor := &AuthInterceptor{cache: make(map[sessionCacheKey]cacheEntry)}
	credential := "cookie:ory_session=raw-secret"
	interceptor.cacheInvalidCredential(credential)

	_, found := interceptor.cache[sessionCredentialCacheKey(credential)]
	require.True(t, found)
	for index := range maxNegativeSessionCacheEntries + 100 {
		interceptor.cacheInvalidCredential("token:unique-" + strconv.Itoa(index))
	}
	require.Len(t, interceptor.cache, maxNegativeSessionCacheEntries)
}

func TestCheckSessionRejectsInvalidCredentialFormat(t *testing.T) {
	interceptor := &AuthInterceptor{
		kratosPublicURL: "http://127.0.0.1",
		httpClient:      &http.Client{Timeout: time.Millisecond},
	}

	session, err := interceptor.checkSession(t.Context(), "malformed")
	require.ErrorContains(t, err, "invalid auth credential format")
	require.Nil(t, session)
}

func TestAuthInterceptorRequestHelpers(t *testing.T) {
	interceptor := &AuthInterceptor{}
	cookieReq := connect.NewRequest(&emptypb.Empty{})
	cookieReq.Header().Set("Cookie", "ory_session=cookie")
	require.Equal(t, "cookie:ory_session=cookie", interceptor.getAuthCredential(cookieReq))

	emptyReq := connect.NewRequest(&emptypb.Empty{})
	require.Empty(t, interceptor.getAuthCredential(emptyReq))

	xffReq := connect.NewRequest(&emptypb.Empty{})
	xffReq.Header().Set("X-Forwarded-For", " 203.0.113.10, 198.51.100.2 ")
	require.Equal(t, "203.0.113.10", interceptor.extractClientIP(xffReq))

	realIPReq := connect.NewRequest(&emptypb.Empty{})
	realIPReq.Header().Set("X-Real-IP", " 203.0.113.20 ")
	require.Equal(t, "203.0.113.20", interceptor.extractClientIP(realIPReq))

	require.True(t, isPublicProcedure("/api.open.v1.PageService/Get"))
	require.False(t, isPublicProcedure("/api.manage.v1.MemberService/Get"))
}
