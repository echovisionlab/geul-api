package workerpostgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/worker"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// MemberDeletionPublisher queues the deletion commands and their
// notification emails (*mq.Publisher).
type MemberDeletionPublisher interface {
	accountpostgres.UserDeletionDispatchPublisher
	accountpostgres.UserDeletionIdentityDispatchPublisher
	accountpostgres.EmailCommandPublisher
}

// memberDeletions calls Account's deletion runtime, which opens its own
// transactions.
type memberDeletions struct {
	db        *gorm.DB
	publisher MemberDeletionPublisher
	identity  auth.IdentityDeleter
	spiceDB   *auth.SpiceDBClient
	lifecycle accountpostgres.MemberDeletion
	audit     domainaudit.Appender
}

func (m memberDeletions) ScrubExpiredEmailProjections(ctx context.Context, now time.Time) (int64, error) {
	return memberpostgres.ScrubExpiredMemberEmailProjections(ctx, m.db, now)
}

func (m memberDeletions) EnqueueExpiredUnonboarded(ctx context.Context, now time.Time) (int, error) {
	return accountpostgres.EnqueueExpiredUnonboardedMembers(
		ctx, m.db, m.publisher, m.lifecycle, now, account.UnonboardedMemberCleanupBatchSize,
	)
}

func (m memberDeletions) DueScheduledDeletions(ctx context.Context, now time.Time) ([]worker.ScheduledDeletion, error) {
	var requests []model.UserDeletionRequest
	if err := m.db.WithContext(ctx).
		Where("lifecycle_state IN ?", []string{"scheduled", "recovery_confirmation_pending"}).
		Where("scheduled_at < ?", now).
		Find(&requests).Error; err != nil {
		return nil, err
	}
	due := make([]worker.ScheduledDeletion, 0, len(requests))
	for _, request := range requests {
		due = append(due, worker.ScheduledDeletion{ID: request.ID, MemberID: request.MemberID, IdentityID: request.IdentityID})
	}
	return due, nil
}

func (m memberDeletions) DispatchScheduledDeletion(ctx context.Context, requestID string, now time.Time) error {
	return accountpostgres.DispatchScheduledUserDeletion(ctx, m.db, m.publisher, m.spiceDB, m.lifecycle, requestID, now)
}

func (m memberDeletions) DeleteIdentity(ctx context.Context, command *managev1.UserDeleteIdentityCommand) error {
	return accountpostgres.ProcessUserDeleteIdentityAudited(
		ctx, m.db, m.identity, m.spiceDB, m.lifecycle, m.publisher, m.audit, command,
	)
}

func (m memberDeletions) DeleteAvatar(ctx context.Context, memberID, avatarAssetID string) error {
	return accountpostgres.ProcessUserDeleteAvatar(ctx, m.db, m.lifecycle, memberID, avatarAssetID)
}

// timedBans clears a timed ban through Account, which rechecks it under the
// identity-state fence.
type timedBans struct {
	db         *gorm.DB
	identities auth.IdentityManager
	audit      domainaudit.Appender
}

func (b timedBans) ClearExpiredTimedBan(ctx context.Context, identityID string, now time.Time) (bool, error) {
	return accountpostgres.ClearExpiredTimedBan(ctx, b.db, b.identities, identityID, now, b.audit)
}
