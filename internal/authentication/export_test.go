package authentication

import "time"

// SetAuthCodeIssuanceClock fixes the clock of l, for the limiter tests in
// package authentication_test.
func SetAuthCodeIssuanceClock(l *AuthCodeIssuanceLimiter, now func() time.Time) { l.now = now }

// AuthCodeQuotaOf returns the client IP quota reservation carries.
func AuthCodeQuotaOf(reservation AuthCodeIssuanceReservation) (limit, remaining, windowSeconds int64) {
	return reservation.quota.limit, reservation.quota.remaining, reservation.quota.windowSeconds
}
