package authentication

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
)

func defaultAuthCodeIssuanceLimits() AuthCodeIssuanceLimits {
	return AuthCodeIssuanceLimits{
		Cooldown:        time.Minute,
		AddressHourly:   6,
		IPTenMinute:     20,
		GlobalPerMinute: 100,
	}
}

// Proxy unit tests exercise request classification and reservation release;
// PostgreSQL budget semantics are covered by the integration-tagged limiter
// tests against the real migration.
func newTestAuthCodeIssuanceLimiter(
	_ *testing.T,
	limits AuthCodeIssuanceLimits,
) (*fakeAuthCodeIssuanceLimiter, struct{}) {
	return &fakeAuthCodeIssuanceLimiter{
		limits:    limits,
		byToken:   make(map[string]string),
		bySubject: make(map[string]string),
	}, struct{}{}
}

type fakeAuthCodeIssuanceLimiter struct {
	mu        sync.Mutex
	limits    AuthCodeIssuanceLimits
	sequence  uint64
	byToken   map[string]string
	bySubject map[string]string
}

func (l *fakeAuthCodeIssuanceLimiter) Reserve(
	_ context.Context,
	request AuthCodeIssuanceRequest,
) (AuthCodeIssuanceReservation, bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	subject := email.NormalizeAddressForDelivery(request.Recipient)
	if subject == "" {
		subject = "flow:" + strings.TrimSpace(request.FlowID)
	}
	if _, exists := l.bySubject[subject]; exists {
		return AuthCodeIssuanceReservation{}, false, l.limits.Cooldown, nil
	}
	if len(l.byToken) >= l.limits.GlobalPerMinute {
		return AuthCodeIssuanceReservation{}, false, time.Minute, nil
	}
	l.sequence++
	reservation := AuthCodeIssuanceReservation{
		token:    fmt.Sprintf("%064x", l.sequence),
		issuedAt: time.Now().UTC(),
	}
	l.byToken[reservation.token] = subject
	l.bySubject[subject] = reservation.token
	return reservation, true, 0, nil
}

func (l *fakeAuthCodeIssuanceLimiter) Release(
	_ context.Context,
	reservation AuthCodeIssuanceReservation,
) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	subject, exists := l.byToken[reservation.token]
	if exists {
		delete(l.byToken, reservation.token)
		delete(l.bySubject, subject)
	}
	return nil
}

func TestAuthCodeIssuanceLimiterValidatesConfigurationAndRequests(t *testing.T) {
	limits := defaultAuthCodeIssuanceLimits()
	db := &gorm.DB{}
	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(nil, []byte("secret"), limits) })
	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(db, nil, limits) })

	invalid := limits
	invalid.GlobalPerMinute = 0
	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(db, []byte("secret"), invalid) })

	limiter := NewAuthCodeIssuanceLimiter(db, []byte("secret"), limits)
	_, _, _, err := limiter.Reserve(t.Context(), AuthCodeIssuanceRequest{
		EventKey:  email.EventWelcome,
		Recipient: "person@example.com",
		ClientIP:  "203.0.113.10",
	})
	require.ErrorContains(t, err, "unsupported auth code event")

	_, _, _, err = limiter.Reserve(t.Context(), AuthCodeIssuanceRequest{
		EventKey: email.EventLoginCode,
		ClientIP: "203.0.113.10",
	})
	require.ErrorContains(t, err, "recipient or flow")

	_, _, _, err = limiter.Reserve(t.Context(), AuthCodeIssuanceRequest{
		EventKey:  email.EventLoginCode,
		Recipient: "person@example.com",
		ClientIP:  "not-an-ip",
	})
	require.ErrorContains(t, err, "client IP")
}

func TestNormalizeAuthCodeClientIP(t *testing.T) {
	require.Equal(t, "203.0.113.10", normalizeAuthCodeClientIP("203.0.113.10:443"))
	require.Equal(t, "2001:db8::1", normalizeAuthCodeClientIP("[2001:db8::1]:443"))
	require.Empty(t, normalizeAuthCodeClientIP("not-an-ip"))
}
