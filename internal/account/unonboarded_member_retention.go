package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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
		executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
		if !ok {
			return fmt.Errorf("database transaction does not expose a PGMQ executor")
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

// EnqueueUnonboardedMemberHardDelete is the admin-requested counterpart to
// the retention scheduler. It records the request actor's scheduled deletion
// transition and the hard-delete command in one transaction, while leaving
// all external cleanup to the worker.
func EnqueueUnonboardedMemberHardDelete(
	ctx context.Context,
	db *gorm.DB,
	publisher UserDeletionIdentityDispatchPublisher,
	members MemberDeletionLifecycle,
	auditWriter domainaudit.Appender,
	memberID string,
) (bool, error) {
	if db == nil || publisher == nil || members == nil || auditWriter == nil {
		return false, fmt.Errorf("unonboarded Member deletion dependencies are required")
	}
	var accepted bool
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The Member owner resolves the current link before the Account fence;
		// the same owner rechecks it after the fence below.
		candidateIdentity, found, err := members.UnonboardedIdentity(ctx, tx, memberID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if candidateIdentity == "" {
			return fmt.Errorf("unonboarded Member has no account identity")
		}
		if err := identitystate.Lock(tx, candidateIdentity); err != nil {
			return err
		}
		eligible, err := members.RecheckUnonboarded(ctx, tx, memberID, candidateIdentity, nil)
		if err != nil || !eligible {
			return err
		}
		executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
		if !ok {
			return fmt.Errorf("database transaction does not expose a PGMQ executor")
		}
		command := &managev1.UserDeleteIdentityCommand{
			Mode:       managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE,
			MemberId:   memberID,
			IdentityId: candidateIdentity,
		}
		if err := publisher.PublishUserDeleteIdentityWithExecutor(ctx, executor, command); err != nil {
			return err
		}
		if err := domainaudit.AppendRequest(
			ctx,
			tx,
			auditWriter,
			sharedtelemetry.AuditAccountUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(
					metadata, memberID, sharedtelemetry.AuditStateNone,
				)
			},
		); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}
