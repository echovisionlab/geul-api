package authentication_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/authentication/authenticationtest"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func issuanceLimits() authentication.AuthCodeIssuanceLimits {
	return authentication.AuthCodeIssuanceLimits{Cooldown: time.Minute, AddressHourly: 6, IPTenMinute: 20, GlobalPerMinute: 100}
}

// newLimiter returns a limiter over a fresh in-memory ledger whose clock
// reads *now.
func newLimiter(limits authentication.AuthCodeIssuanceLimits, now *time.Time) (*authentication.AuthCodeIssuanceLimiter, *authenticationtest.Store, *mutationtest.UnitOfWork[authentication.Store]) {
	store, units := authenticationtest.New()
	limiter := authentication.NewAuthCodeIssuanceLimiter(units, []byte("test-key-secret"), limits)
	if now != nil {
		authentication.SetAuthCodeIssuanceClock(limiter, func() time.Time { return *now })
	}
	return limiter, store, units
}

func TestAuthCodeIssuanceLimiterRejectsBeforeSecondIssuance(t *testing.T) {
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, store, units := newLimiter(issuanceLimits(), &now)
	request := authentication.AuthCodeIssuanceRequest{
		EventKey: email.EventLoginCode, Recipient: " Person@Example.COM ", FlowID: "login-flow", ClientIP: "203.0.113.10:443",
	}

	reservation, allowed, retryAfter, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Zero(t, retryAfter)
	require.Len(t, reservation.IssuanceID(), 64)
	require.Equal(t, now, reservation.IssuedAt())
	limit, remaining, window := authentication.AuthCodeQuotaOf(reservation)
	require.Equal(t, []int64{20, 19, 600}, []int64{limit, remaining, window})

	rejected, allowed, retryAfter, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, time.Minute, retryAfter)
	require.Empty(t, rejected.IssuanceID())
	rejectedLimit, rejectedRemaining, rejectedWindow := authentication.AuthCodeQuotaOf(rejected)
	require.Equal(t, []int64{limit, remaining, window}, []int64{rejectedLimit, rejectedRemaining, rejectedWindow})
	require.Equal(t, []mutation.Step{mutation.StepCommit, mutation.StepCommit}, units.Steps(), "a rejection commits nothing it wrote")

	issuances := store.Issuances()
	require.Len(t, issuances, 1)
	stored := issuances[0]
	require.Equal(t, reservation.IssuanceID(), stored.Token)
	require.Equal(t, email.EventLoginCode, stored.Purpose)
	require.Equal(t, now.Add(time.Hour), stored.ExpiresAt, "a row outlives the longest budget window")
	persisted := stored.RecipientDigest + stored.SubjectDigest + stored.ClientIPDigest
	require.NotContains(t, persisted, "person@example.com")
	require.NotContains(t, persisted, "203.0.113.10")
	require.NotContains(t, persisted, "login-flow")
	require.Equal(t, stored.RecipientDigest, stored.SubjectDigest, "a recipient is its own budget subject")
}

func TestAuthCodeIssuanceLimiterReleaseRestoresAllBudgets(t *testing.T) {
	limits := issuanceLimits()
	limits.GlobalPerMinute = 1
	limiter, store, _ := newLimiter(limits, nil)
	request := authentication.AuthCodeIssuanceRequest{
		EventKey: email.EventVerificationCode, Recipient: "person@example.com", FlowID: "verification-flow", ClientIP: "2001:db8::1",
	}

	reservation, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, limiter.Release(t.Context(), reservation))
	require.Empty(t, store.Issuances())

	_, allowed, _, err = limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestAuthCodeIssuanceLimiterReleaseRejectsAForgedReservation(t *testing.T) {
	limiter, _, units := newLimiter(issuanceLimits(), nil)

	require.ErrorContains(t, limiter.Release(t.Context(), authentication.AuthCodeIssuanceReservation{}), "reservation is invalid")
	require.Empty(t, units.Steps())
}

func TestAuthCodeIssuanceLimiterAdmitsOneOfManyWithinTheGlobalLimit(t *testing.T) {
	limits := issuanceLimits()
	limits.GlobalPerMinute = 1
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, store, _ := newLimiter(limits, &now)

	type outcome struct {
		allowed    bool
		retryAfter time.Duration
		err        error
	}
	const contenders = 8
	results := make(chan outcome, contenders)
	var workers sync.WaitGroup
	for index := range contenders {
		workers.Go(func() {
			_, allowed, retryAfter, err := limiter.Reserve(t.Context(), authentication.AuthCodeIssuanceRequest{
				EventKey: email.EventLoginCode, Recipient: fmt.Sprintf("person-%d@example.com", index),
				FlowID: fmt.Sprintf("flow-%d", index), ClientIP: fmt.Sprintf("203.0.113.%d", index+1),
			})
			results <- outcome{allowed: allowed, retryAfter: retryAfter, err: err}
		})
	}
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
	require.Len(t, store.Issuances(), 1)
}

func TestAuthCodeIssuanceLimiterCurrentReservationIsPurposeAndRecipientBound(t *testing.T) {
	issuedAt := time.Date(2026, time.July, 31, 10, 0, 0, 123456789, time.UTC)
	limiter, _, _ := newLimiter(issuanceLimits(), &issuedAt)
	request := authentication.AuthCodeIssuanceRequest{
		EventKey: email.EventVerificationCode, Recipient: " Person@Example.COM ", FlowID: "settings-flow", ClientIP: "203.0.113.10",
	}

	reserved, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, issuedAt.Truncate(time.Microsecond), reserved.IssuedAt())
	current, found, err := limiter.CurrentReservation(t.Context(), email.EventVerificationCode, "person@example.com")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, reserved.IssuanceID(), current.IssuanceID())
	require.Equal(t, reserved.IssuedAt(), current.IssuedAt())

	_, _, err = limiter.CurrentReservation(t.Context(), email.EventLoginCode, "person@example.com")
	require.ErrorContains(t, err, "reservation is invalid")
	_, found, err = limiter.CurrentReservation(t.Context(), email.EventVerificationCode, "other@example.com")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, limiter.Release(t.Context(), reserved))
	_, found, err = limiter.CurrentReservation(t.Context(), email.EventVerificationCode, "person@example.com")
	require.NoError(t, err)
	require.False(t, found)
}

func TestAuthCodeIssuanceLimiterCurrentReservationRejectsATamperedRecord(t *testing.T) {
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, store, _ := newLimiter(issuanceLimits(), &now)
	reserved, allowed, _, err := limiter.Reserve(t.Context(), authentication.AuthCodeIssuanceRequest{
		EventKey: email.EventVerificationCode, Recipient: "person@example.com", FlowID: "settings-flow", ClientIP: "203.0.113.10",
	})
	require.NoError(t, err)
	require.True(t, allowed)
	stored := store.Issuances()[0]

	for name, tamper := range map[string]func(*authentication.Issuance){
		"purpose":     func(issuance *authentication.Issuance) { issuance.Purpose = email.EventLoginCode },
		"token":       func(issuance *authentication.Issuance) { issuance.Token = strings.ToUpper(issuance.Token) },
		"issued time": func(issuance *authentication.Issuance) { issuance.IssuedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := stored
			tamper(&tampered)
			replace(t, store, reserved.IssuanceID(), tampered)
			_, found, err := limiter.CurrentReservation(t.Context(), email.EventVerificationCode, "person@example.com")
			require.Error(t, err)
			require.False(t, found)
			replace(t, store, tampered.Token, stored)
		})
	}
}

// replace swaps the ledger row token for issuance, as a writer outside the
// limiter would.
func replace(t *testing.T, store *authenticationtest.Store, token string, issuance authentication.Issuance) {
	t.Helper()
	require.NoError(t, store.DeleteIssuance(t.Context(), token))
	require.NoError(t, store.InsertIssuance(t.Context(), issuance))
}

func TestAuthCodeIssuanceLimiterEnforcesAddressBudgetAcrossPurposes(t *testing.T) {
	limits := issuanceLimits()
	limits.Cooldown = time.Millisecond
	limits.AddressHourly = 2
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, _, _ := newLimiter(limits, &now)
	request := authentication.AuthCodeIssuanceRequest{Recipient: "person@example.com", FlowID: "flow-1", ClientIP: "203.0.113.10"}

	for _, eventKey := range []email.EventKey{email.EventLoginCode, email.EventVerificationCode} {
		request.EventKey = eventKey
		_, allowed, _, err := limiter.Reserve(t.Context(), request)
		require.NoError(t, err)
		require.True(t, allowed)
		now = now.Add(time.Millisecond)
	}

	request.EventKey = email.EventRegistrationCode
	_, allowed, retryAfter, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, time.Hour-2*time.Millisecond, retryAfter, "the oldest issuance of the hour leaves the window first")
}

func TestAuthCodeIssuanceLimiterUsesOneCooldownForLoginAndRegistration(t *testing.T) {
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, _, _ := newLimiter(issuanceLimits(), &now)
	request := authentication.AuthCodeIssuanceRequest{
		EventKey: email.EventLoginCode, Recipient: "person@example.com", FlowID: "login-flow", ClientIP: "203.0.113.10",
	}

	_, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)

	now = now.Add(20 * time.Second)
	request.EventKey, request.FlowID = email.EventRegistrationCode, "registration-flow"
	_, allowed, retryAfter, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, 40*time.Second, retryAfter)
}

func TestAuthCodeIssuanceLimiterLimitsAFlowWithoutRecipientByTheFlow(t *testing.T) {
	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	limiter, store, _ := newLimiter(issuanceLimits(), &now)
	request := authentication.AuthCodeIssuanceRequest{EventKey: email.EventRegistrationCode, FlowID: "registration-flow", ClientIP: "203.0.113.11"}

	_, allowed, _, err := limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)
	_, allowed, _, err = limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.False(t, allowed, "the flow is the cooldown subject")
	request.FlowID = "other-flow"
	_, allowed, _, err = limiter.Reserve(t.Context(), request)
	require.NoError(t, err)
	require.True(t, allowed)

	issuances := store.Issuances()
	require.NotEqual(t, issuances[0].SubjectDigest, issuances[1].SubjectDigest)
	require.Equal(t, issuances[0].RecipientDigest, issuances[1].RecipientDigest, "both digest the missing recipient alike")
}

func TestAuthCodeIssuanceLimiterValidatesConfigurationAndRequests(t *testing.T) {
	limits := issuanceLimits()
	_, units := authenticationtest.New()
	require.Panics(t, func() { authentication.NewAuthCodeIssuanceLimiter(nil, []byte("secret"), limits) })
	require.Panics(t, func() { authentication.NewAuthCodeIssuanceLimiter(units, nil, limits) })
	invalid := limits
	invalid.GlobalPerMinute = 0
	require.Panics(t, func() { authentication.NewAuthCodeIssuanceLimiter(units, []byte("secret"), invalid) })

	limiter := authentication.NewAuthCodeIssuanceLimiter(units, []byte("secret"), limits)
	for message, request := range map[string]authentication.AuthCodeIssuanceRequest{
		"unsupported auth code event": {EventKey: email.EventWelcome, Recipient: "person@example.com", ClientIP: "203.0.113.10"},
		"recipient or flow":           {EventKey: email.EventLoginCode, ClientIP: "203.0.113.10"},
		"client IP":                   {EventKey: email.EventLoginCode, Recipient: "person@example.com", ClientIP: "not-an-ip"},
	} {
		_, _, _, err := limiter.Reserve(t.Context(), request)
		require.ErrorContains(t, err, message)
	}
	require.Empty(t, units.Steps(), "an invalid request opens no transaction")
}

func TestAuthCodeAdmissionHeadersDiscloseOnlyTheClientIPBudget(t *testing.T) {
	limiter, _, _ := newLimiter(issuanceLimits(), nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":"sent_email"}`)
	}))
	defer upstream.Close()
	proxy, err := authentication.NewKratosPublicProxy(upstream.URL, limiter, []byte("auth-issuance-provenance-key"))
	require.NoError(t, err)
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		request := httptest.NewRequest(http.MethodPost, "/self-service/login?flow=login-flow", strings.NewReader(`{"method":"code","identifier":"person@example.com"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		require.Equal(t, status, response.Code, response.Body.String())
		require.Equal(t, `"auth-code-ip";q=20;w=600`, response.Header().Get("RateLimit-Policy"))
		require.Equal(t, `"auth-code-ip";r=19;t=600`, response.Header().Get("RateLimit"))
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		if status == http.StatusTooManyRequests {
			require.NotEmpty(t, response.Header().Get("Retry-After"))
		}
	}
}
