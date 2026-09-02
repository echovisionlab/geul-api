package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/requestip"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
)

// kratosSession represents the relevant fields from Kratos /sessions/whoami response.
type kratosSession struct {
	ID     string `json:"id"`
	Active bool   `json:"active"`
}

type cacheEntry struct {
	expiresAt time.Time
}

type sessionCacheKey [sha256.Size]byte

// AuthInterceptor is a Connect RPC unary interceptor that validates
// authentication. Public requests use the Oathkeeper-projected Kratos session
// ID; cluster-internal Web requests may use the raw Kratos session cookie.
type AuthInterceptor struct {
	kratosPublicURL string
	httpClient      *http.Client
	db              *gorm.DB
	geoipLookup     *geoip.Lookup

	mu    sync.RWMutex
	cache map[sessionCacheKey]cacheEntry
}

const cacheTTL = 30 * time.Second
const cleanupInterval = 5 * time.Minute
const maxNegativeSessionCacheEntries = 4096

// ErrSessionPrincipalInvalid means the session ID is malformed, inactive,
// expired, or no longer resolves through an active Identity to a live Member.
var ErrSessionPrincipalInvalid = errors.New("session principal is invalid")

// ErrPrincipalResolutionUnavailable means the canonical account/member lookup
// could not be completed because the authoritative database was unavailable.
// It is deliberately distinct from ErrSessionPrincipalInvalid: dependency
// failures must not be reported to callers as an invalid credential.
var ErrPrincipalResolutionUnavailable = errors.New("canonical principal resolution unavailable")

// NewAuthInterceptor creates an interceptor. Call Start with the process
// context to run cache cleanup for the interceptor's lifecycle.
func NewAuthInterceptor(kratosPublicURL string, db *gorm.DB) *AuthInterceptor {
	if strings.TrimSpace(kratosPublicURL) == "" {
		panic("Kratos public URL is required")
	}
	ai := &AuthInterceptor{
		kratosPublicURL: kratosPublicURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		db:          db,
		geoipLookup: geoip.NewLookup(db),
		cache:       make(map[sessionCacheKey]cacheEntry),
	}
	return ai
}

// Start removes expired session cache entries until ctx is cancelled.
func (ai *AuthInterceptor) Start(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			ai.removeExpiredCacheEntries(now)
		}
	}
}

func (ai *AuthInterceptor) removeExpiredCacheEntries(now time.Time) {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	for key, entry := range ai.cache {
		if !now.Before(entry.expiresAt) {
			delete(ai.cache, key)
		}
	}
}

func sessionCredentialCacheKey(credential string) sessionCacheKey {
	return sha256.Sum256([]byte(credential))
}

func (ai *AuthInterceptor) isNegativeCached(credential string) bool {
	ai.mu.RLock()
	defer ai.mu.RUnlock()
	entry, ok := ai.cache[sessionCredentialCacheKey(credential)]
	return ok && time.Now().Before(entry.expiresAt)
}

func (ai *AuthInterceptor) cacheInvalidCredential(credential string) {
	now := time.Now()
	ai.mu.Lock()
	defer ai.mu.Unlock()
	for key, entry := range ai.cache {
		if !now.Before(entry.expiresAt) {
			delete(ai.cache, key)
		}
	}
	if len(ai.cache) >= maxNegativeSessionCacheEntries {
		var oldestKey sessionCacheKey
		var oldestExpiry time.Time
		for key, entry := range ai.cache {
			if oldestExpiry.IsZero() || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = entry.expiresAt
			}
		}
		delete(ai.cache, oldestKey)
	}
	ai.cache[sessionCredentialCacheKey(credential)] = cacheEntry{
		expiresAt: now.Add(cacheTTL),
	}
}

// WrapUnary implements connect.Interceptor.
func (ai *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		// Extract client IP and lookup GeoIP
		clientIP := ai.extractClientIP(req)
		if geoInfo := ai.geoipLookup.LookupIP(ctx, clientIP); geoInfo != nil {
			ctx = geoip.WithInfo(ctx, geoInfo)
		}
		// Oathkeeper projects the current Kratos session ID. The database resolves
		// and validates the session -> identity -> member chain. Cluster-internal
		// Web callers may use the direct cookie/session transport below.
		if sessionID := req.Header().Get("X-Session-Id"); sessionID != "" {
			return ai.handleOathkeeperSession(ctx, req, next, procedure)
		}

		// Direct access mode - do authentication only.

		// Cluster-internal Web callers use the raw Kratos session cookie. Public
		// callers must arrive through Oathkeeper with X-Session-Id.
		authCred := ai.getAuthCredential(req)

		// Public endpoints: attempt auth but don't require it.
		if isPublicProcedure(procedure) {
			return ai.handleDirectPublicRequest(ctx, req, next, authCred)
		}

		// All other endpoints require authentication
		if authCred == "" {
			return nil, errs.AuthenticationRequired()
		}

		user, resolveErr := ai.resolveUserWithError(ctx, authCred)
		if resolveErr != nil {
			if errors.Is(resolveErr, ErrSessionPrincipalInvalid) {
				return nil, errs.InvalidSession()
			}
			return nil, errs.DependencyUnavailable("authentication")
		}

		if user.Banned {
			return nil, errs.AccountBanned()
		}
		if !user.Onboarded && !isNicknameOnboardingProcedure(procedure) {
			return nil, errs.FailedPrecondition("member onboarding must be completed")
		}

		ctx = WithUser(ctx, user)
		return next(ctx, req)
	}
}

func (ai *AuthInterceptor) handleOathkeeperSession(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
	procedure string,
) (connect.AnyResponse, error) {
	if req.Header().Get("X-Session-Id") == "" {
		return handleMissingGatewaySession(ctx, req, next, procedure)
	}
	// Oathkeeper has already called Kratos /sessions/whoami, so a verified
	// assertion represents an active identity.
	user, err := ai.userFromOathkeeperAssertion(ctx, req)
	if err != nil {
		if errors.Is(err, ErrSessionPrincipalInvalid) {
			return nil, errs.InvalidSession()
		}
		return nil, errs.DependencyUnavailable("principal database")
	}
	if user.Banned {
		return nil, errs.AccountBanned()
	}
	if !user.Onboarded {
		return handleUnonboardedGatewayUser(ctx, req, next, procedure, user)
	}
	return next(WithUser(ctx, user), req)
}

func handleMissingGatewaySession(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
	procedure string,
) (connect.AnyResponse, error) {
	if isPublicProcedure(procedure) {
		return next(ctx, req)
	}
	return nil, errs.InvalidSession()
}

func handleUnonboardedGatewayUser(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
	procedure string,
	user *UserInfo,
) (connect.AnyResponse, error) {
	if isPublicProcedure(procedure) {
		// An unfinished account retains the anonymous public surface.
		return next(ctx, req)
	}
	if !isNicknameOnboardingProcedure(procedure) {
		return nil, errs.FailedPrecondition("member onboarding must be completed")
	}
	return next(WithUser(ctx, user), req)
}

func (ai *AuthInterceptor) handleDirectPublicRequest(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
	authCredential string,
) (connect.AnyResponse, error) {
	if authCredential == "" {
		return next(ctx, req)
	}
	user, err := ai.resolveUserWithError(ctx, authCredential)
	if err != nil {
		if errors.Is(err, ErrSessionPrincipalInvalid) {
			return next(ctx, req)
		}
		return nil, errs.DependencyUnavailable("authentication")
	}
	if user == nil || !user.Onboarded {
		return next(ctx, req)
	}
	return next(WithUser(ctx, user), req)
}

// userFromOathkeeperAssertion resolves the asserted session in a single
// database query. No identity, member, role, or profile header participates in
// auth context.
func (ai *AuthInterceptor) userFromOathkeeperAssertion(ctx context.Context, req connect.AnyRequest) (*UserInfo, error) {
	principal, err := ResolveAuthenticatedPrincipalBySessionID(
		ctx,
		ai.db,
		req.Header().Get("X-Session-Id"),
	)
	return principal, err
}

// WrapStreamingClient implements connect.Interceptor (no-op for server-side).
func (ai *AuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor (no-op for now).
func (ai *AuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (ai *AuthInterceptor) getAuthCredential(req connect.AnyRequest) string {
	if cookie := req.Header().Get("Cookie"); cookie != "" {
		return "cookie:" + cookie
	}
	return ""
}

func isPublicProcedure(procedure string) bool {
	return strings.HasPrefix(procedure, "/api.open.v1.")
}

func isNicknameOnboardingProcedure(procedure string) bool {
	switch procedure {
	case "/api.manage.v1.MemberService/GetCurrentSession",
		"/api.manage.v1.MemberService/CheckNicknameAvailability",
		"/api.manage.v1.MemberService/CompleteMyOnboarding":
		return true
	default:
		return false
	}
}

func (ai *AuthInterceptor) resolveUserWithError(ctx context.Context, authCred string) (*UserInfo, error) {
	if ai.isNegativeCached(authCred) {
		return nil, ErrSessionPrincipalInvalid
	}

	// Call Kratos whoami
	session, err := ai.checkSession(ctx, authCred)
	if err != nil {
		slog.Warn("Kratos session check failed", "error", err)
		if errors.Is(err, ErrSessionPrincipalInvalid) {
			ai.cacheInvalidCredential(authCred)
		}
		return nil, err
	}
	if !session.Active {
		ai.cacheInvalidCredential(authCred)
		return nil, ErrSessionPrincipalInvalid
	}

	// Extract user info from Kratos session
	user, err := ResolveAuthenticatedPrincipalBySessionID(ctx, ai.db, session.ID)
	if err != nil {
		slog.Warn("Authenticated session principal validation failed", "error", err)
		if errors.Is(err, ErrSessionPrincipalInvalid) {
			ai.cacheInvalidCredential(authCred)
		}
		return nil, err
	}

	return user, nil
}

func (ai *AuthInterceptor) checkSession(ctx context.Context, authCred string) (*kratosSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ai.kratosPublicURL+"/sessions/whoami", nil)
	if err != nil {
		return nil, err
	}

	// Only the cluster-internal Web cookie fallback is accepted here.
	if len(authCred) > 7 && authCred[:7] == "cookie:" {
		req.Header.Set("Cookie", authCred[7:])
	} else {
		return nil, fmt.Errorf("%w: invalid auth credential format", ErrSessionPrincipalInvalid)
	}

	resp, err := ai.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kratos request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w: session not valid", ErrSessionPrincipalInvalid)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kratos returned %d: %s", resp.StatusCode, string(body))
	}

	var session kratosSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("failed to decode kratos response: %w", err)
	}
	return &session, nil
}

type authenticatedPrincipalRow struct {
	SessionID       string    `gorm:"column:session_id"`
	IdentityID      string    `gorm:"column:identity_id"`
	MemberID        string    `gorm:"column:member_id"`
	AuthenticatedAt time.Time `gorm:"column:authenticated_at"`
	Banned          bool      `gorm:"column:banned"`
	Onboarded       bool      `gorm:"column:onboarded"`
}

// ResolveAuthenticatedPrincipalBySessionID validates an active Kratos session
// and resolves its linked Identity and Member in one database query.
func ResolveAuthenticatedPrincipalBySessionID(ctx context.Context, db *gorm.DB, sessionID string) (*UserInfo, error) {
	return resolveAuthenticatedPrincipalBySessionID(ctx, db, sessionID, false)
}

// LockAuthenticatedSessionForPrincipal rechecks and locks the exact active
// collaboration session after the caller has fenced the derived Identity and
// Member. This order avoids inverting account deletion's Identity-to-session
// lock order while preserving sign-first session revocation exclusion.
func LockAuthenticatedSessionForPrincipal(
	ctx context.Context,
	tx *gorm.DB,
	sessionID string,
	expected *UserInfo,
) error {
	if expected == nil {
		return ErrSessionPrincipalInvalid
	}
	current, err := resolveAuthenticatedPrincipalBySessionID(ctx, tx, sessionID, true)
	if err != nil {
		return err
	}
	if current == nil ||
		current.SessionID != expected.SessionID ||
		current.IdentityID != expected.IdentityID ||
		current.MemberID != expected.MemberID {
		return ErrSessionPrincipalInvalid
	}
	return nil
}

func resolveAuthenticatedPrincipalBySessionID(
	ctx context.Context,
	db *gorm.DB,
	sessionID string,
	lockSession bool,
) (*UserInfo, error) {
	if db == nil {
		return nil, fmt.Errorf("member link database is required")
	}
	parsedSession, err := uuidutil.ParseCanonical(sessionID, "session_id")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionPrincipalInvalid, err)
	}
	query := `
		SELECT
			session.id::text AS session_id,
			 i.id::text AS identity_id,
			m.id::text AS member_id,
			session.authenticated_at AS authenticated_at,
			COALESCE((i.metadata_admin ->> 'banned')::boolean, false) AS banned,
			m.onboarded AS onboarded
		FROM kratos.sessions AS session
		JOIN kratos.identities AS i
		  ON i.id = session.identity_id
		JOIN member AS m
		  ON m.account_identity_id = i.id
		 AND m.id::text = i.external_id
		WHERE session.id = ?::uuid
		  AND session.active = TRUE
		  AND session.expires_at > NOW()
		  AND i.state = 'active'
		  AND m.deleted_at IS NULL
		LIMIT 1
`
	if lockSession && db.Dialector.Name() == "postgres" {
		query += "\t\tFOR SHARE OF session\n"
	}
	var row authenticatedPrincipalRow
	if err := db.WithContext(ctx).Raw(query, parsedSession.String()).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("validate authenticated principal: %w", err)
	}
	if row.SessionID == "" || row.IdentityID == "" || row.MemberID == "" {
		return nil, ErrSessionPrincipalInvalid
	}
	return &UserInfo{
		IdentityID:      IdentityID(row.IdentityID),
		MemberID:        MemberID(row.MemberID),
		SessionID:       SessionID(row.SessionID),
		AuthenticatedAt: row.AuthenticatedAt,
		Authenticated:   true,
		Banned:          row.Banned,
		Onboarded:       row.Onboarded,
	}, nil
}

// extractClientIP extracts the client IP from request headers.
// Checks X-Forwarded-For, X-Real-IP, then falls back to peer address.
func (ai *AuthInterceptor) extractClientIP(req connect.AnyRequest) string {
	return requestip.TrustedClientIP(
		req.Header().Get("X-Forwarded-For"),
		req.Header().Get("X-Real-IP"),
		req.Peer().Addr,
	)
}
