package authentication

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
// the limiter's budgets are tested on the in-memory ledger in
// auth_code_issuance_limiter_test.go, and the ledger's SQL by the Store
// contract.
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

func TestNormalizeAuthCodeClientIP(t *testing.T) {
	require.Equal(t, "203.0.113.10", normalizeAuthCodeClientIP("203.0.113.10:443"))
	require.Equal(t, "2001:db8::1", normalizeAuthCodeClientIP("[2001:db8::1]:443"))
	require.Empty(t, normalizeAuthCodeClientIP("not-an-ip"))
}
