package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	UnonboardedMemberRetention        = 7 * 24 * time.Hour
	UnonboardedMemberCleanupBatchSize = 100
)

// EnqueueExpiredUnonboardedMembers scans the derived retention predicate and
// durably enqueues one hard-delete command per linked Member. The queue row is
// written through the same transaction that locks and rechecks the Member, so
// onboarding cannot race a command into the queue after the predicate ceased
// to hold. There is deliberately no cleanup intent row: the Member and its
// linked account identity remain the only product facts.
func EnqueueExpiredUnonboardedMembers(
	ctx context.Context,
	units Units,
	now time.Time,
	pageSize int,
) (int, error) {
	if units == nil {
		return 0, fmt.Errorf("expired unonboarded cleanup requires transactional user deletion publisher")
	}
	if pageSize <= 0 {
		return 0, nil
	}

	cutoff := now.UTC().Add(-UnonboardedMemberRetention)
	queued := 0
	var enqueueErrs []error
	var cursor *MemberUnonboardedCursor
	for {
		var (
			candidates []MemberUnonboardedCandidate
			next       *MemberUnonboardedCursor
		)
		if err := units.Read(ctx, func(store Store) error {
			var err error
			candidates, next, err = store.ExpiredUnonboardedMembers(ctx, cutoff, pageSize, cursor)
			return err
		}); err != nil {
			return queued, err
		}
		for i := range candidates {
			accepted, err := enqueueExpiredUnonboardedMember(ctx, units, candidates[i], cutoff)
			if err != nil {
				enqueueErrs = append(enqueueErrs, fmt.Errorf("enqueue unonboarded Member %s: %w", candidates[i].MemberID, err))
				continue
			}
			if accepted {
				queued++
			}
		}
		if len(candidates) < pageSize || next == nil {
			break
		}
		cursor = next
	}
	return queued, errors.Join(enqueueErrs...)
}

// enqueueExpiredUnonboardedMember locks the candidate's Identity, rechecks
// the retention predicate and enqueues its hard-delete command in one
// transaction. It writes no audit record and answers to no principal.
func enqueueExpiredUnonboardedMember(
	ctx context.Context,
	units Units,
	candidate MemberUnonboardedCandidate,
	cutoff time.Time,
) (bool, error) {
	var accepted bool
	err := units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		if err := store.LockIdentity(ctx, candidate.IdentityID); err != nil {
			return err
		}
		eligible, err := store.RecheckUnonboardedMember(ctx, candidate.MemberID, candidate.IdentityID, &cutoff)
		if err != nil || !eligible {
			return err
		}
		if err := store.EnqueueUserDeleteIdentity(ctx, DeleteIdentityCommand{
			Mode:       DeleteIdentityUnonboardedHardDelete,
			MemberID:   candidate.MemberID,
			IdentityID: candidate.IdentityID,
		}); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}

// enqueueUnonboardedHardDelete is the admin-requested counterpart to the
// retention scheduler. It records the administrator's scheduled deletion
// transition and the hard-delete command in one mutation, while leaving all
// external cleanup to the worker. It reports false when the Member is not an
// unonboarded one, so the caller tombstones it instead.
func (s *Service) enqueueUnonboardedHardDelete(ctx context.Context, memberID string) (bool, error) {
	// The Member owner resolves the current link before the Account fence;
	// the same owner rechecks it after the fence.
	var (
		identityID string
		found      bool
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		identityID, found, err = store.UnonboardedMemberIdentity(ctx, memberID)
		return err
	}); err != nil || !found {
		return false, err
	}
	if identityID == "" {
		return false, errors.New("unonboarded Member has no account identity")
	}
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, store Store) (bool, error) {
			return store.RecheckUnonboardedMember(ctx, memberID, identityID, nil)
		},
		Audit: func(eligible bool) mutation.Audit {
			if !eligible {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(metadata, memberID, sharedtelemetry.AuditStateNone)
			})
		},
		Outbox: func(ctx context.Context, store Store, eligible bool) error {
			if !eligible {
				return nil
			}
			return store.EnqueueUserDeleteIdentity(ctx, DeleteIdentityCommand{
				Mode:       DeleteIdentityUnonboardedHardDelete,
				MemberID:   memberID,
				IdentityID: identityID,
			})
		},
	})
}
