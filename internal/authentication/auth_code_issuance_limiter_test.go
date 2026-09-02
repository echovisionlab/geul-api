//go:build integration

package authentication

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
)

func newIntegrationAuthCodeIssuanceLimiter(
	t *testing.T,
	limits AuthCodeIssuanceLimits,
) (*AuthCodeIssuanceLimiter, *gorm.DB) {
	t.Helper()
	db := newConcurrentAuthenticationIntegrationDB(t)
	require.NoError(t, db.Exec("DELETE FROM public.auth_code_issuance").Error)
	limiter := NewAuthCodeIssuanceLimiter(db, []byte("test-key-secret"), limits)
	return limiter, db
}

func TestAuthCodeIssuanceLimiterRejectsBeforeSecondIssuance(t *testing.T) {
	limiter, db := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	request := AuthCodeIssuanceRequest{
		EventKey:  email.EventLoginCode,
		Recipient: " Person@Example.COM ",
		FlowID:    "login-flow",
		ClientIP:  "203.0.113.10:443",
	}

	reservation, allowed, retryAfter, err := limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Zero(t, retryAfter)
	require.NotEmpty(t, reservation.token)

	_, allowed, retryAfter, err = limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, time.Minute, retryAfter)

	var persisted string
	require.NoError(t, db.Raw(`
		SELECT recipient_digest || subject_digest || client_ip_digest
		FROM public.auth_code_issuance
		LIMIT 1`).Scan(&persisted).Error)
	require.NotContains(t, persisted, "person@example.com")
	require.NotContains(t, persisted, "203.0.113.10")
	require.NotContains(t, persisted, "login-flow")
}

func TestAuthCodeIssuanceLimiterReleaseRestoresAllBudgets(t *testing.T) {
	limits := defaultAuthCodeIssuanceLimits()
	limits.GlobalPerMinute = 1
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, limits)
	request := AuthCodeIssuanceRequest{
		EventKey:  email.EventVerificationCode,
		Recipient: "person@example.com",
		FlowID:    "verification-flow",
		ClientIP:  "2001:db8::1",
	}

	reservation, allowed, _, err := limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, limiter.Release(context.Background(), reservation))

	_, allowed, _, err = limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestAuthCodeIssuanceLimiterSerializesConcurrentGlobalLimit(t *testing.T) {
	limits := defaultAuthCodeIssuanceLimits()
	limits.GlobalPerMinute = 1
	limiter, db := newIntegrationAuthCodeIssuanceLimiter(t, limits)
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	type outcome struct {
		allowed    bool
		retryAfter time.Duration
		err        error
	}
	const contenders = 8
	start := make(chan struct{})
	results := make(chan outcome, contenders)
	var workers sync.WaitGroup
	for index := 0; index < contenders; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, allowed, retryAfter, err := limiter.Reserve(t.Context(), AuthCodeIssuanceRequest{
				EventKey:  email.EventLoginCode,
				Recipient: fmt.Sprintf("person-%d@example.com", index),
				FlowID:    fmt.Sprintf("flow-%d", index),
				ClientIP:  fmt.Sprintf("203.0.113.%d", index+1),
			})
			results <- outcome{allowed: allowed, retryAfter: retryAfter, err: err}
		}(index)
	}

	close(start)
	workers.Wait()
	close(results)

	allowedCount := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.allowed {
			allowedCount++
			require.Zero(t, result.retryAfter)
			continue
		}
		require.Equal(t, time.Minute, result.retryAfter)
	}
	require.Equal(t, 1, allowedCount)

	var persisted int64
	require.NoError(t, db.Model(&struct{}{}).
		Table("public.auth_code_issuance").
		Count(&persisted).Error)
	require.Equal(t, int64(1), persisted)
}

func TestDeleteExpiredAuthCodeIssuancesIsBounded(t *testing.T) {
	_, db := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`
		INSERT INTO public.auth_code_issuance (
			token, purpose, recipient_digest, subject_digest,
			client_ip_digest, issued_at, expires_at
		)
		SELECT
			lpad(to_hex(sequence), 64, '0'),
			'login_code',
			repeat('a', 64),
			repeat('b', 64),
			repeat('c', 64),
			?,
			?
		FROM generate_series(1, ?) AS generated(sequence)`,
		now.Add(-2*time.Minute),
		now.Add(-time.Minute),
		authCodeIssuanceCleanupBatchSize+1,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.auth_code_issuance (
			token, purpose, recipient_digest, subject_digest,
			client_ip_digest, issued_at, expires_at
		) VALUES (?, 'login_code', ?, ?, ?, ?, ?)`,
		fmt.Sprintf("%064x", authCodeIssuanceCleanupBatchSize+2),
		fmt.Sprintf("%064x", 4),
		fmt.Sprintf("%064x", 5),
		fmt.Sprintf("%064x", 6),
		now,
		now.Add(time.Hour),
	).Error)

	deleted, err := deleteExpiredAuthCodeIssuances(db, now, authCodeIssuanceCleanupBatchSize)
	require.NoError(t, err)
	require.Equal(t, int64(authCodeIssuanceCleanupBatchSize), deleted)

	var expired int64
	require.NoError(t, db.Model(&struct{}{}).
		Table("public.auth_code_issuance").
		Where("expires_at <= ?", now).
		Count(&expired).Error)
	require.Equal(t, int64(1), expired)

	deleted, err = deleteExpiredAuthCodeIssuances(db, now, authCodeIssuanceCleanupBatchSize)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)

	var total int64
	require.NoError(t, db.Model(&struct{}{}).
		Table("public.auth_code_issuance").
		Count(&total).Error)
	require.Equal(t, int64(1), total)
}

func TestAuthCodeIssuanceLimiterCurrentReservationIsPurposeAndRecipientBound(t *testing.T) {
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	issuedAt := time.Date(2026, time.July, 31, 10, 0, 0, 123456789, time.UTC)
	limiter.now = func() time.Time { return issuedAt }
	request := AuthCodeIssuanceRequest{
		EventKey:  email.EventVerificationCode,
		Recipient: " Person@Example.COM ",
		FlowID:    "settings-flow",
		ClientIP:  "203.0.113.10",
	}

	reserved, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	current, found, err := limiter.CurrentReservation(
		t.Context(), email.EventVerificationCode, "person@example.com",
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, reserved.token, current.token)
	require.Equal(t, reserved.issuedAt, current.issuedAt)

	_, _, err = limiter.CurrentReservation(
		t.Context(), email.EventLoginCode, "person@example.com",
	)
	require.ErrorContains(t, err, "reservation is invalid")
	_, found, err = limiter.CurrentReservation(
		t.Context(), email.EventVerificationCode, "other@example.com",
	)
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, limiter.Release(t.Context(), reserved))
	_, found, err = limiter.CurrentReservation(
		t.Context(), email.EventVerificationCode, "person@example.com",
	)
	require.NoError(t, err)
	require.False(t, found)
}

func TestAuthCodeIssuanceLimiterCurrentReservationRejectsTamperedAndMissingRecord(t *testing.T) {
	limiter, db := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	request := AuthCodeIssuanceRequest{
		EventKey:  email.EventVerificationCode,
		Recipient: "person@example.com",
		FlowID:    "settings-flow",
		ClientIP:  "203.0.113.10",
	}
	reserved, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)

	require.NoError(t, db.Exec(
		"UPDATE public.auth_code_issuance SET purpose = ? WHERE token = ?",
		email.EventLoginCode.String(), reserved.token,
	).Error)
	_, _, err = limiter.CurrentReservation(
		t.Context(), email.EventVerificationCode, request.Recipient,
	)
	require.ErrorContains(t, err, "reservation is invalid")

	require.NoError(t, db.Exec(
		"DELETE FROM public.auth_code_issuance WHERE token = ?",
		reserved.token,
	).Error)
	_, found, err := limiter.CurrentReservation(
		t.Context(), email.EventVerificationCode, request.Recipient,
	)
	require.NoError(t, err)
	require.False(t, found)
}

func TestAuthCodeIssuanceLimiterEnforcesAddressBudgetAcrossPurposes(t *testing.T) {
	limits := defaultAuthCodeIssuanceLimits()
	limits.Cooldown = time.Millisecond
	limits.AddressHourly = 2
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, limits)
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	base := AuthCodeIssuanceRequest{
		Recipient: "person@example.com",
		FlowID:    "flow-1",
		ClientIP:  "203.0.113.10",
	}

	base.EventKey = email.EventLoginCode
	_, allowed, _, err := limiter.Reserve(context.Background(), base)
	require.NoError(t, err)
	require.True(t, allowed)
	now = now.Add(time.Millisecond)

	base.EventKey = email.EventVerificationCode
	_, allowed, _, err = limiter.Reserve(context.Background(), base)
	require.NoError(t, err)
	require.True(t, allowed)
	now = now.Add(time.Millisecond)

	base.EventKey = email.EventRegistrationCode
	_, allowed, retryAfter, err := limiter.Reserve(context.Background(), base)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Greater(t, retryAfter, 59*time.Minute)
}

func TestAuthCodeIssuanceLimiterUsesOneCooldownForLoginAndRegistration(t *testing.T) {
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	request := AuthCodeIssuanceRequest{
		EventKey:  email.EventLoginCode,
		Recipient: "person@example.com",
		FlowID:    "login-flow",
		ClientIP:  "203.0.113.10",
	}

	_, allowed, _, err := limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.True(t, allowed)

	request.EventKey = email.EventRegistrationCode
	request.FlowID = "registration-flow"
	_, allowed, retryAfter, err := limiter.Reserve(context.Background(), request)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, time.Minute, retryAfter)
}

func TestAuthCodeIssuanceLimiterAcceptsFlowWhenRecipientIsUnavailable(t *testing.T) {
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())

	_, allowed, _, err := limiter.Reserve(context.Background(), AuthCodeIssuanceRequest{
		EventKey: email.EventRegistrationCode,
		FlowID:   "registration-flow",
		ClientIP: "203.0.113.11",
	})
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestAuthCodeIssuanceLimiterRejectsInvalidRequest(t *testing.T) {
	limiter, _ := newIntegrationAuthCodeIssuanceLimiter(t, defaultAuthCodeIssuanceLimits())

	_, _, _, err := limiter.Reserve(context.Background(), AuthCodeIssuanceRequest{
		EventKey:  email.EventWelcome,
		Recipient: "person@example.com",
		ClientIP:  "203.0.113.11",
	})
	require.ErrorContains(t, err, "unsupported auth code event")

	_, _, _, err = limiter.Reserve(context.Background(), AuthCodeIssuanceRequest{
		EventKey: email.EventLoginCode,
		ClientIP: "203.0.113.11",
	})
	require.ErrorContains(t, err, "recipient or flow")

	_, _, _, err = limiter.Reserve(context.Background(), AuthCodeIssuanceRequest{
		EventKey:  email.EventLoginCode,
		Recipient: "person@example.com",
		ClientIP:  "not-an-ip",
	})
	require.ErrorContains(t, err, "client IP")
}

func TestNewAuthCodeIssuanceLimiterRejectsInvalidConfiguration(t *testing.T) {
	db := newConcurrentAuthenticationIntegrationDB(t)
	valid := defaultAuthCodeIssuanceLimits()

	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(nil, []byte("secret"), valid) })
	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(db, nil, valid) })
	invalid := valid
	invalid.IPTenMinute = 0
	require.Panics(t, func() { NewAuthCodeIssuanceLimiter(db, []byte("secret"), invalid) })
}
