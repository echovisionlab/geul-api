package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
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
	db *gorm.DB,
	publisher UserDeletionIdentityDispatchPublisher,
	members MemberDeletionLifecycle,
	now time.Time,
	pageSize int,
) (int, error) {
	if db == nil || publisher == nil || members == nil {
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
		candidates, next, err := members.ListExpiredUnonboarded(ctx, db, cutoff, pageSize, cursor)
		if err != nil {
			return queued, err
		}
		for i := range candidates {
			accepted, err := enqueueExpiredUnonboardedMember(ctx, db, publisher, members, candidates[i], cutoff)
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

func enqueueExpiredUnonboardedMember(
	ctx context.Context,
	db *gorm.DB,
	publisher UserDeletionIdentityDispatchPublisher,
	members MemberDeletionLifecycle,
	candidate MemberUnonboardedCandidate,
	cutoff time.Time,
) (bool, error) {
	if publisher == nil {
		return false, fmt.Errorf("transactional user deletion publisher is required")
	}
	var accepted bool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, candidate.IdentityID); err != nil {
			return err
		}
		eligible, err := members.RecheckUnonboarded(ctx, tx, candidate.MemberID, candidate.IdentityID, &cutoff)
		if err != nil || !eligible {
			return err
		}
		executor, err := contentsignal.TransactionExecutor(tx)
		if err != nil {
			return err
		}
		command := &managev1.UserDeleteIdentityCommand{
			Mode:       managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE,
			MemberId:   candidate.MemberID,
			IdentityId: candidate.IdentityID,
		}
		if err := publisher.PublishUserDeleteIdentityWithExecutor(ctx, executor, command); err != nil {
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
	identityID, found, err := s.memberDeletion.UnonboardedIdentity(ctx, s.db, memberID)
	if err != nil || !found {
		return false, err
	}
	if identityID == "" {
		return false, errors.New("unonboarded Member has no account identity")
	}
	return mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, tx *gorm.DB) (bool, error) {
			return s.memberDeletion.RecheckUnonboarded(ctx, tx, memberID, identityID, nil)
		},
		Audit: func(eligible bool) mutation.Audit {
			if !eligible {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(metadata, memberID, sharedtelemetry.AuditStateNone)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, eligible bool) error {
			if !eligible {
				return nil
			}
			executor, err := contentsignal.TransactionExecutor(tx)
			if err != nil {
				return err
			}
			return s.publisher.PublishUserDeleteIdentityWithExecutor(ctx, executor, &managev1.UserDeleteIdentityCommand{
				Mode:       managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE,
				MemberId:   memberID,
				IdentityId: identityID,
			})
		},
	})
}
