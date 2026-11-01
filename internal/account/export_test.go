package account

import (
	"context"
	"time"
)

// The clocks and bounds the use-case tests in account_test reach.

const (
	AccountEmailChangeProofGrace      = accountEmailChangeProofGrace
	AccountEmailChangePendingLifetime = accountEmailChangePendingLifetime
	AccountEmailChangeBatchSize       = accountEmailChangeBatchSize
)

// SetNow replaces the email change lifecycle's clock.
func (s *AccountEmailChangeLifecycle) SetNow(now func() time.Time) { s.now = now }

// SetNow replaces the Account use cases' clock.
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// DeliveryPrimaryEmailForIdentity reads the Member primary email security
// settings compare with the canonical email.
func (s *Service) DeliveryPrimaryEmailForIdentity(ctx context.Context, memberID, identityID string) (string, error) {
	return s.deliveryPrimaryEmailForIdentity(ctx, memberID, identityID)
}

// Credentials is the credential mutation service of the Account use cases.
func (s *Service) Credentials() *AccountCredentialMutationService { return s.credentials() }

// ScheduleImmediateDeletion schedules an administrator's immediate deletion
// of a Member.
func (s *Service) ScheduleImmediateDeletion(ctx context.Context, memberID, identityID string) error {
	return s.scheduleImmediateDeletion(ctx, memberID, identityID)
}
