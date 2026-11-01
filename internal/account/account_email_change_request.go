package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

var (
	ErrAccountEmailChangeConflict = errors.New("account email change conflicts with another identity")
	ErrAccountEmailChangeInFlight = errors.New("verified account email change reconciliation is in progress")
	// ErrAccountEmailChangeNotificationPublish means the identity and account
	// projection already converged, but the durable notification command was not
	// confirmed. The active request remains the retry authority.
	ErrAccountEmailChangeNotificationPublish = errors.New("account email change notification was not published")
)

const (
	accountEmailChangeReconcileInterval = time.Minute
	accountEmailChangeProofGrace        = 15 * time.Minute
	// An unverified pending address holds that address in Kratos for every
	// account, so an exact pending request expires after this lifetime.
	accountEmailChangePendingLifetime = 7 * 24 * time.Hour
	accountEmailChangeBatchSize       = 50
)

type accountEmailChangeIdentityManager interface {
	auth.IdentityGetter
	auth.IdentityCredentialFinder
	auth.IdentityAccountEmailStateWriter
}

// AccountEmailChangeLifecycle converges active email-change requests from
// Kratos' canonical, pending and verified address facts. It does not persist a
// copy of the derived phase or PGMQ delivery state.
type AccountEmailChangeLifecycle struct {
	units     Units
	identity  accountEmailChangeIdentityManager
	publisher EmailPublisher
	now       func() time.Time
	interval  time.Duration
}

// transact runs work in a transaction of its own on the pool.
func (s *AccountEmailChangeLifecycle) transact(ctx context.Context, work func(Store) error) error {
	return s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error { return work(tx.Store()) })
}

// AuthorizeSettingsGeneratedVerification verifies the narrow Kratos v26.2
// settings courier fallback. Canonical changes require their active request.
func (s *AccountEmailChangeLifecycle) AuthorizeSettingsGeneratedVerification(
	ctx context.Context,
	identityID string,
	recipient string,
) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	recipient = normalizeAccountEmail(recipient)
	if identityID == "" || recipient == "" {
		return false, nil
	}

	identity, err := LoadIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil || identity.ID != identityID ||
		identity.HasVerifiedEmailAddress(recipient) ||
		!identity.HasUnverifiedEmailAddress(recipient) {
		return false, nil
	}
	var request *EmailChangeRequest
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		request, err = store.EmailChangeRequestTo(ctx, identityID, recipient)
		return err
	}); err != nil {
		return false, err
	}
	if request == nil {
		return false, nil
	}
	if !accountEmailsEqual(identity.CurrentEmail(), request.PreviousEmailAddress) {
		return false, nil
	}
	pending := normalizeAccountEmail(identity.PendingEmail())
	return pending == "" || accountEmailsEqual(pending, recipient), nil
}

func NewAccountEmailChangeLifecycle(
	units Units,
	identity accountEmailChangeIdentityManager,
	publisher EmailPublisher,
) *AccountEmailChangeLifecycle {
	if units == nil {
		panic("account email change lifecycle: units are required")
	}
	if identity == nil {
		panic("account email change lifecycle: identity manager is required")
	}
	if publisher == nil {
		panic("account email change lifecycle: publisher is required")
	}
	lifecycle := &AccountEmailChangeLifecycle{
		units:     units,
		identity:  identity,
		publisher: publisher,
		now:       time.Now,
		interval:  accountEmailChangeReconcileInterval,
	}
	return lifecycle
}

func normalizeAccountEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func accountEmailsEqual(a, b string) bool {
	return normalizeAccountEmail(a) == normalizeAccountEmail(b)
}

func validateAccountEmailLength(value string) error {
	if utf8.RuneCountInString(value) > 254 {
		return fmt.Errorf("account email exceeds 254 characters")
	}
	return nil
}
