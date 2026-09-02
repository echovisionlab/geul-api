package authentication

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
)

// One short transaction serializes the four overlapping budgets. The lock is
// transaction-scoped, so PostgreSQL releases it on commit, rollback, or a lost
// connection without a lease-renewal loop.
const (
	authCodeIssuanceAdvisoryLockID   int64 = 4923378199947301957
	authCodeIssuanceCleanupBatchSize int   = 1000
)

type AuthCodeIssuanceLimits struct {
	Cooldown        time.Duration
	AddressHourly   int
	IPTenMinute     int
	GlobalPerMinute int
}

type AuthCodeIssuanceRequest struct {
	EventKey  email.EventKey
	Recipient string
	FlowID    string
	ClientIP  string
}

type AuthCodeIssuanceReservation struct {
	token    string
	issuedAt time.Time
}

// IssuanceID returns the opaque reservation identifier used to bind the
// courier provenance to the accepted public issuance.
func (r AuthCodeIssuanceReservation) IssuanceID() string { return r.token }

// IssuedAt returns the exact issuance instant used for courier expiry.
func (r AuthCodeIssuanceReservation) IssuedAt() time.Time { return r.issuedAt }

type authCodeIssuanceBudget struct {
	limit  int
	window time.Duration
	column string
	digest string
}

type AuthCodeIssuanceLimiter struct {
	db        *gorm.DB
	keySecret []byte
	limits    AuthCodeIssuanceLimits
	now       func() time.Time
}

func NewAuthCodeIssuanceLimiter(
	db *gorm.DB,
	keySecret []byte,
	limits AuthCodeIssuanceLimits,
) *AuthCodeIssuanceLimiter {
	if db == nil {
		panic("auth code issuance limiter database is required")
	}
	if len(keySecret) == 0 {
		panic("auth code issuance limiter key secret is required")
	}
	if limits.Cooldown <= 0 || limits.AddressHourly <= 0 || limits.IPTenMinute <= 0 || limits.GlobalPerMinute <= 0 {
		panic("auth code issuance limits must be positive")
	}
	return &AuthCodeIssuanceLimiter{
		db:        db,
		keySecret: append([]byte(nil), keySecret...),
		limits:    limits,
		now:       time.Now,
	}
}

func (l *AuthCodeIssuanceLimiter) Reserve(
	ctx context.Context,
	request AuthCodeIssuanceRequest,
) (AuthCodeIssuanceReservation, bool, time.Duration, error) {
	if l == nil {
		return AuthCodeIssuanceReservation{}, false, 0, errors.New("auth code issuance limiter is required")
	}
	if !isAuthCodeEvent(request.EventKey) {
		return AuthCodeIssuanceReservation{}, false, 0, fmt.Errorf("unsupported auth code event: %q", request.EventKey)
	}
	recipient := email.NormalizeAddressForDelivery(request.Recipient)
	flowID := strings.TrimSpace(request.FlowID)
	clientIP := normalizeAuthCodeClientIP(request.ClientIP)
	if recipient == "" && flowID == "" {
		return AuthCodeIssuanceReservation{}, false, 0, errors.New("auth code recipient or flow is required")
	}
	if clientIP == "" {
		return AuthCodeIssuanceReservation{}, false, 0, errors.New("auth code client IP is required")
	}

	subject := recipient
	if subject == "" {
		subject = "flow:" + flowID
	}
	subjectDigest := l.digest(subject)
	clientIPDigest := l.digest(clientIP)
	budgets := []authCodeIssuanceBudget{
		{limit: l.limits.GlobalPerMinute, window: time.Minute},
		{limit: l.limits.IPTenMinute, window: 10 * time.Minute, column: "client_ip_digest", digest: clientIPDigest},
		{limit: l.limits.AddressHourly, window: time.Hour, column: "subject_digest", digest: subjectDigest},
		// Login and registration are intentionally the same public operation.
		// Purpose remains provenance, never an address-existence side channel.
		{limit: 1, window: l.limits.Cooldown, column: "subject_digest", digest: subjectDigest},
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return AuthCodeIssuanceReservation{}, false, 0, fmt.Errorf("generate auth code issuance token: %w", err)
	}
	now := l.now().UTC().Truncate(time.Microsecond)
	reservation := AuthCodeIssuanceReservation{
		token:    hex.EncodeToString(tokenBytes),
		issuedAt: now,
	}
	recipientDigest := l.digest(recipient)
	allowed := false
	retryAfter := time.Duration(0)
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", authCodeIssuanceAdvisoryLockID).Error; err != nil {
			return err
		}
		for _, budget := range budgets {
			candidate, err := authCodeIssuanceRetryAfter(tx, budget, now)
			if err != nil {
				return err
			}
			if candidate > retryAfter {
				retryAfter = candidate
			}
		}
		if retryAfter > 0 {
			return nil
		}
		if err := tx.Exec(`
			INSERT INTO public.auth_code_issuance (
				token, purpose, recipient_digest, subject_digest,
				client_ip_digest, issued_at, expires_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			reservation.token,
			request.EventKey.String(),
			recipientDigest,
			subjectDigest,
			clientIPDigest,
			now,
			now.Add(authCodeIssuanceRecordTTL(l.limits)),
		).Error; err != nil {
			return err
		}
		allowed = true
		return nil
	})
	if err != nil {
		return AuthCodeIssuanceReservation{}, false, 0, fmt.Errorf("reserve auth code issuance: %w", err)
	}
	if !allowed {
		return AuthCodeIssuanceReservation{}, false, retryAfter, nil
	}
	return reservation, true, 0, nil
}

func deleteExpiredAuthCodeIssuances(tx *gorm.DB, now time.Time, limit int) (int64, error) {
	if tx == nil {
		return 0, errors.New("auth code issuance cleanup database is required")
	}
	if limit <= 0 {
		return 0, errors.New("auth code issuance cleanup limit must be positive")
	}
	result := tx.Exec(`
		WITH expired AS (
			SELECT token
			FROM public.auth_code_issuance
			WHERE expires_at <= ?
			ORDER BY expires_at, token
			LIMIT ?
		)
		DELETE FROM public.auth_code_issuance AS issuance
		USING expired
		WHERE issuance.token = expired.token`, now, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("delete expired auth code issuances: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// CleanupExpiredAuthCodeIssuances physically removes a bounded batch of
// logically expired limiter rows. It is safe to run alongside Reserve because
// active budget queries ignore expired windows and the delete targets only
// expires_at <= now.
func CleanupExpiredAuthCodeIssuances(ctx context.Context, db *gorm.DB, now time.Time, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("auth code issuance cleanup database is required")
	}
	return deleteExpiredAuthCodeIssuances(db.WithContext(ctx), now.UTC(), limit)
}

func authCodeIssuanceRetryAfter(
	tx *gorm.DB,
	budget authCodeIssuanceBudget,
	now time.Time,
) (time.Duration, error) {
	query := "SELECT count(*), min(issued_at) FROM public.auth_code_issuance WHERE issued_at > ?"
	args := []any{now.Add(-budget.window)}
	if budget.column != "" {
		query += " AND " + budget.column + " = ?"
		args = append(args, budget.digest)
	}
	var count int64
	var first sql.NullTime
	if err := tx.Raw(query, args...).Row().Scan(&count, &first); err != nil {
		return 0, err
	}
	if count < int64(budget.limit) {
		return 0, nil
	}
	if !first.Valid {
		return budget.window, nil
	}
	retryAfter := budget.window - now.Sub(first.Time.UTC())
	if retryAfter <= 0 {
		return time.Microsecond, nil
	}
	return retryAfter, nil
}

func (l *AuthCodeIssuanceLimiter) Release(
	ctx context.Context,
	reservation AuthCodeIssuanceReservation,
) error {
	if l == nil {
		return errors.New("auth code issuance limiter is required")
	}
	if !validAuthCodeIssuanceID(reservation.token) {
		return errors.New("auth code issuance reservation is invalid")
	}
	if err := l.db.WithContext(ctx).
		Exec("DELETE FROM public.auth_code_issuance WHERE token = ?", reservation.token).Error; err != nil {
		return fmt.Errorf("release auth code issuance: %w", err)
	}
	return nil
}

// CurrentReservation returns the exact purpose-bound limiter reservation for
// one recipient. The HMAC-derived key binds the recipient without storing it.
func (l *AuthCodeIssuanceLimiter) CurrentReservation(
	ctx context.Context,
	eventKey email.EventKey,
	recipient string,
) (AuthCodeIssuanceReservation, bool, error) {
	if l == nil {
		return AuthCodeIssuanceReservation{}, false, errors.New("auth code issuance limiter is required")
	}
	recipient = email.NormalizeAddressForDelivery(recipient)
	if recipient == "" {
		return AuthCodeIssuanceReservation{}, false, errors.New("auth code recipient is required")
	}
	if !isAuthCodeEvent(eventKey) {
		return AuthCodeIssuanceReservation{}, false, fmt.Errorf("unsupported auth code event: %q", eventKey)
	}
	recipientDigest := l.digest(recipient)
	var record struct {
		Token           string
		Purpose         string
		RecipientDigest string
		IssuedAt        time.Time
	}
	err := l.db.WithContext(ctx).Raw(`
		SELECT token, purpose, recipient_digest, issued_at
		FROM public.auth_code_issuance
		WHERE recipient_digest = ? AND expires_at > ?
		ORDER BY issued_at DESC
		LIMIT 1`, recipientDigest, l.now().UTC()).Scan(&record).Error
	if err != nil {
		return AuthCodeIssuanceReservation{}, false, fmt.Errorf("load auth code issuance reservation: %w", err)
	}
	if record.Token == "" {
		return AuthCodeIssuanceReservation{}, false, nil
	}
	if !validAuthCodeIssuanceID(record.Token) ||
		record.Purpose != eventKey.String() ||
		record.RecipientDigest != recipientDigest {
		return AuthCodeIssuanceReservation{}, false, errors.New("auth code issuance reservation is invalid")
	}
	if record.IssuedAt.IsZero() {
		return AuthCodeIssuanceReservation{}, false, errors.New("auth code issuance reservation time is invalid")
	}
	return AuthCodeIssuanceReservation{
		token:    record.Token,
		issuedAt: record.IssuedAt.UTC(),
	}, true, nil
}

func authCodeIssuanceRecordTTL(limits AuthCodeIssuanceLimits) time.Duration {
	ttl := limits.Cooldown
	for _, candidate := range []time.Duration{time.Hour, 10 * time.Minute, time.Minute} {
		if candidate > ttl {
			ttl = candidate
		}
	}
	return ttl
}

func (l *AuthCodeIssuanceLimiter) digest(value string) string {
	mac := hmac.New(sha256.New, l.keySecret)
	_, _ = mac.Write([]byte(strings.TrimSpace(strings.ToLower(value))))
	return hex.EncodeToString(mac.Sum(nil))
}

func normalizeAuthCodeClientIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip := net.ParseIP(strings.Trim(value, "[]"))
	if ip == nil {
		return ""
	}
	return ip.String()
}

func isAuthCodeEvent(eventKey email.EventKey) bool {
	switch eventKey {
	case email.EventLoginCode, email.EventRegistrationCode, email.EventVerificationCode:
		return true
	default:
		return false
	}
}
