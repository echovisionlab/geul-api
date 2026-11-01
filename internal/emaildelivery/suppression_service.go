package emaildelivery

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// GetEmailSuppressionQuery reads the active suppression of an address.
type GetEmailSuppressionQuery struct {
	Email string
}

// GetEmailSuppression returns the active suppression of the address, or nil
// when deliveries to it are not suppressed.
func (s *Service) GetEmailSuppression(ctx context.Context, query GetEmailSuppressionQuery) (*Suppression, error) {
	// A suppression names an address and its delivery failure; only
	// administrators may read it.
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	if query.Email == "" {
		return nil, domainerr.Required("email")
	}
	var suppression *Suppression
	err := s.units.Read(ctx, func(store Store) (err error) {
		suppression, err = FindActiveSuppression(ctx, store, query.Email)
		return err
	})
	return suppression, err
}

// ReleaseEmailSuppressionCommand releases the active suppression of an
// address.
type ReleaseEmailSuppressionCommand struct {
	Email string
}

// ReleaseEmailSuppression releases the active suppression of the address,
// recording the releasing Member. Releasing an address without one changes
// nothing.
func (s *Service) ReleaseEmailSuppression(ctx context.Context, command ReleaseEmailSuppressionCommand) error {
	var releasedBy *string
	if user := auth.GetUser(ctx); user != nil && user.MemberID.String() != "" {
		memberID := user.MemberID.String()
		releasedBy = &memberID
	}
	normalized := emailutil.NormalizeAddressForDelivery(command.Email)
	var suppression Suppression
	found := false
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Lock: func(ctx context.Context, store Store) error {
			if normalized == "" {
				return nil
			}
			locked, err := store.LockActiveSuppression(ctx, normalized)
			if errors.Is(err, ErrSuppressionNotFound) {
				return nil
			}
			suppression, found = locked, err == nil
			return err
		},
		Access: freshAdmin(policyv1.EmailSuppression.Release()),
		Change: func(ctx context.Context, store Store) (bool, error) {
			if normalized == "" {
				return false, domainerr.Required("email")
			}
			if !found {
				return false, nil
			}
			return true, store.ReleaseSuppression(ctx, suppression.ID, s.now().UTC(), releasedBy)
		},
		Audit: func(released bool) mutation.Audit {
			if !released {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditEmailSuppressionUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewEmailSuppressionReleasedAuditRecord(metadata, suppression.ID)
			})
		},
	})
	return err
}
