package authentication

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	quota    authCodeQuota
}

// IssuanceID returns the opaque reservation identifier used to bind the
// courier provenance to the accepted public issuance.
func (r AuthCodeIssuanceReservation) IssuanceID() string { return r.token }

// IssuedAt returns the exact issuance instant used for courier expiry.
func (r AuthCodeIssuanceReservation) IssuedAt() time.Time { return r.issuedAt }

type authCodeQuota struct {
	limit         int64
	remaining     int64
	windowSeconds int64
}

type authCodeIssuanceBudget struct {
	limit  int
	window time.Duration
	scope  IssuanceScope
	digest string
}

// AuthCodeIssuanceLimiter reserves public auth code issuances against four
// overlapping budgets. A reservation is admission bookkeeping for an
// anonymous request, not a mutation of domain state: there is no principal
// to fence or authorize and no audit record, so it runs in its own unit of
// work instead of mutation.Run.
type AuthCodeIssuanceLimiter struct {
	units     mutation.UnitOfWork[Store]
	keySecret []byte
	limits    AuthCodeIssuanceLimits
	now       func() time.Time
}

func NewAuthCodeIssuanceLimiter(
	units mutation.UnitOfWork[Store],
	keySecret []byte,
	limits AuthCodeIssuanceLimits,
) *AuthCodeIssuanceLimiter {
	if units == nil {
		panic("auth code issuance limiter unit of work is required")
	}
	if len(keySecret) == 0 {
		panic("auth code issuance limiter key secret is required")
	}
	if limits.Cooldown <= 0 || limits.AddressHourly <= 0 || limits.IPTenMinute <= 0 || limits.GlobalPerMinute <= 0 {
		panic("auth code issuance limits must be positive")
	}
	return &AuthCodeIssuanceLimiter{
		units:     units,
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
	issuance, err := l.newIssuance(request)
	if err != nil {
		return AuthCodeIssuanceReservation{}, false, 0, err
	}
	reservation := AuthCodeIssuanceReservation{token: issuance.token, issuedAt: issuance.issuedAt}
	allowed := false
	retryAfter := time.Duration(0)
	// One short transaction serializes the four overlapping budgets.
	err = l.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		if err := store.LockIssuances(ctx); err != nil {
			return err
		}
		var err error
		if retryAfter, reservation.quota, err = l.spendBudgets(ctx, store, issuance); err != nil || retryAfter > 0 {
			return err
		}
		if err := store.InsertIssuance(ctx, l.issuanceRow(issuance)); err != nil {
			return err
		}
		reservation.quota.remaining = max(0, reservation.quota.remaining-1)
		allowed = true
		return nil
	})
	if err != nil {
		return AuthCodeIssuanceReservation{}, false, 0, fmt.Errorf("reserve auth code issuance: %w", err)
	}
	if !allowed {
		return AuthCodeIssuanceReservation{quota: reservation.quota}, false, retryAfter, nil
	}
	return reservation, true, 0, nil
}

// authCodeIssuance is one requested issuance with its keyed digests.
type authCodeIssuance struct {
	request         AuthCodeIssuanceRequest
	token           string
	issuedAt        time.Time
	recipientDigest string
	subjectDigest   string
	clientIPDigest  string
}

func (l *AuthCodeIssuanceLimiter) newIssuance(request AuthCodeIssuanceRequest) (authCodeIssuance, error) {
	if !isAuthCodeEvent(request.EventKey) {
		return authCodeIssuance{}, fmt.Errorf("unsupported auth code event: %q", request.EventKey)
	}
	recipient := email.NormalizeAddressForDelivery(request.Recipient)
	flowID := strings.TrimSpace(request.FlowID)
	clientIP := normalizeAuthCodeClientIP(request.ClientIP)
	if recipient == "" && flowID == "" {
		return authCodeIssuance{}, errors.New("auth code recipient or flow is required")
	}
	if clientIP == "" {
		return authCodeIssuance{}, errors.New("auth code client IP is required")
	}
	subject := recipient
	if subject == "" {
		subject = "flow:" + flowID
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return authCodeIssuance{}, fmt.Errorf("generate auth code issuance token: %w", err)
	}
	return authCodeIssuance{
		request:         request,
		token:           hex.EncodeToString(tokenBytes),
		issuedAt:        l.now().UTC().Truncate(time.Microsecond),
		recipientDigest: l.digest(recipient),
		subjectDigest:   l.digest(subject),
		clientIPDigest:  l.digest(clientIP),
	}, nil
}

// spendBudgets reports how long issuance must wait for its tightest
// exhausted budget, zero when every budget has room, and the client IP
// budget's quota.
func (l *AuthCodeIssuanceLimiter) spendBudgets(ctx context.Context, store Store, issuance authCodeIssuance) (time.Duration, authCodeQuota, error) {
	budgets := []authCodeIssuanceBudget{
		{limit: l.limits.GlobalPerMinute, window: time.Minute, scope: EveryIssuance},
		{limit: l.limits.IPTenMinute, window: 10 * time.Minute, scope: ClientIPIssuances, digest: issuance.clientIPDigest},
		{limit: l.limits.AddressHourly, window: time.Hour, scope: SubjectIssuances, digest: issuance.subjectDigest},
		// Login and registration are intentionally the same public operation.
		// Purpose remains provenance, never an address-existence side channel.
		{limit: 1, window: l.limits.Cooldown, scope: SubjectIssuances, digest: issuance.subjectDigest},
	}
	var quota authCodeQuota
	retryAfter := time.Duration(0)
	for _, budget := range budgets {
		candidate, count, err := authCodeIssuanceRetryAfter(ctx, store, budget, issuance.issuedAt)
		if err != nil {
			return 0, authCodeQuota{}, err
		}
		if budget.scope == ClientIPIssuances {
			quota = authCodeQuota{
				limit:         int64(budget.limit),
				remaining:     max(0, int64(budget.limit)-count),
				windowSeconds: int64(budget.window / time.Second),
			}
		}
		retryAfter = max(retryAfter, candidate)
	}
	return retryAfter, quota, nil
}

func (l *AuthCodeIssuanceLimiter) issuanceRow(issuance authCodeIssuance) Issuance {
	return Issuance{
		Token:           issuance.token,
		Purpose:         issuance.request.EventKey,
		RecipientDigest: issuance.recipientDigest,
		SubjectDigest:   issuance.subjectDigest,
		ClientIPDigest:  issuance.clientIPDigest,
		IssuedAt:        issuance.issuedAt,
		ExpiresAt:       issuance.issuedAt.Add(authCodeIssuanceRecordTTL(l.limits)),
	}
}

func authCodeIssuanceRetryAfter(
	ctx context.Context,
	store Store,
	budget authCodeIssuanceBudget,
	now time.Time,
) (time.Duration, int64, error) {
	usage, err := store.IssuanceUsage(ctx, IssuanceUsageQuery{Scope: budget.scope, Digest: budget.digest, After: now.Add(-budget.window)})
	if err != nil {
		return 0, 0, err
	}
	if usage.Count < int64(budget.limit) {
		return 0, usage.Count, nil
	}
	if usage.First.IsZero() {
		return budget.window, usage.Count, nil
	}
	retryAfter := budget.window - now.Sub(usage.First.UTC())
	if retryAfter <= 0 {
		return time.Microsecond, usage.Count, nil
	}
	return retryAfter, usage.Count, nil
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
	if err := l.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		return tx.Store().DeleteIssuance(ctx, reservation.token)
	}); err != nil {
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
	var record IssuedCode
	var found bool
	err := l.units.Read(ctx, func(store Store) error {
		var err error
		record, found, err = store.LatestIssuance(ctx, recipientDigest, l.now().UTC())
		return err
	})
	if err != nil {
		return AuthCodeIssuanceReservation{}, false, fmt.Errorf("load auth code issuance reservation: %w", err)
	}
	if !found {
		return AuthCodeIssuanceReservation{}, false, nil
	}
	if !validAuthCodeIssuanceID(record.Token) ||
		record.Purpose != eventKey ||
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
