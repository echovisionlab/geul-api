package accountadapter

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
)

// MemberDeletion delegates the Member-owned portions of an Account lifecycle
// command without making Account depend on the Member package.
type MemberDeletion struct{}

var _ accountpostgres.MemberDeletion = MemberDeletion{}

func (MemberDeletion) PrepareTombstone(
	ctx context.Context, db *gorm.DB, memberID, identityID, email string,
) (account.MemberDeletionSnapshot, error) {
	snapshot, err := (member.DeletionLifecycle{}).PrepareTombstone(ctx, memberpostgres.Bind(db), memberID, identityID, email)
	return account.MemberDeletionSnapshot{
		MemberID: snapshot.MemberID, IdentityID: snapshot.IdentityID,
		PrimaryEmailSnapshot: snapshot.PrimaryEmailSnapshot,
		AlreadyTombstoned:    snapshot.AlreadyTombstoned, IdentityLinked: snapshot.IdentityLinked,
	}, err
}

func (MemberDeletion) FinalizeTombstone(
	ctx context.Context,
	db *gorm.DB,
	snapshot account.MemberDeletionSnapshot,
	deletedAt time.Time,
	appendAudit accountpostgres.MemberDeletionAudit,
) error {
	store, err := memberpostgres.BindTransaction(db)
	if err != nil {
		return err
	}
	return (member.DeletionLifecycle{}).FinalizeTombstone(ctx, store, member.DeletionSnapshot{
		MemberID: snapshot.MemberID, IdentityID: snapshot.IdentityID,
		PrimaryEmailSnapshot: snapshot.PrimaryEmailSnapshot,
		AlreadyTombstoned:    snapshot.AlreadyTombstoned, IdentityLinked: snapshot.IdentityLinked,
	}, deletedAt, deletionAudit(db, appendAudit))
}

func (MemberDeletion) PrepareUnonboardedHardDelete(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (account.MemberUnonboardedDeletionTarget, error) {
	target, err := (member.DeletionLifecycle{}).PrepareUnonboardedHardDelete(ctx, memberpostgres.Bind(db), memberID, identityID)
	return account.MemberUnonboardedDeletionTarget{
		MemberID: target.MemberID, IdentityID: target.IdentityID, IdentityLinked: target.IdentityLinked,
	}, err
}

func (MemberDeletion) RevokeUnonboardedAuthorization(
	ctx context.Context,
	spicedb *auth.SpiceDBClient,
	target account.MemberUnonboardedDeletionTarget,
) error {
	return (member.DeletionLifecycle{}).RevokeUnonboardedAuthorization(ctx, spicedb, unonboardedTarget(target))
}

func (MemberDeletion) FinalizeUnonboardedHardDelete(
	ctx context.Context,
	tx *gorm.DB,
	target account.MemberUnonboardedDeletionTarget,
	appendAudit accountpostgres.MemberDeletionAudit,
) error {
	store, err := memberpostgres.BindTransaction(tx)
	if err != nil {
		return err
	}
	return (member.DeletionLifecycle{}).FinalizeUnonboardedHardDelete(ctx, store, unonboardedTarget(target), deletionAudit(tx, appendAudit))
}

// deletionAudit binds Account's audit callback to the transaction the Member
// step runs in.
func deletionAudit(tx *gorm.DB, appendAudit accountpostgres.MemberDeletionAudit) member.DeletionAudit {
	if appendAudit == nil {
		return nil
	}
	return func(ctx context.Context, memberID string) error { return appendAudit(ctx, tx, memberID) }
}

func unonboardedTarget(target account.MemberUnonboardedDeletionTarget) member.UnonboardedDeletionTarget {
	return member.UnonboardedDeletionTarget{MemberID: target.MemberID, IdentityID: target.IdentityID, IdentityLinked: target.IdentityLinked}
}

func (MemberDeletion) CleanupAvatar(ctx context.Context, db *gorm.DB, memberID, avatarAssetID string) error {
	return (member.DeletionLifecycle{}).CleanupAvatar(ctx, memberpostgres.Bind(db), memberID, avatarAssetID)
}

func (MemberDeletion) NotificationSnapshot(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (account.MemberDeletionNotification, error) {
	snapshot, err := (member.DeletionLifecycle{}).NotificationSnapshot(ctx, memberpostgres.Bind(db), memberID, identityID)
	return account.MemberDeletionNotification{
		Nickname: snapshot.Nickname, PrimaryEmail: snapshot.PrimaryEmail, Locale: snapshot.Locale,
	}, err
}

func (MemberDeletion) CompletionEligible(ctx context.Context, db *gorm.DB, memberID string) (bool, error) {
	return (member.DeletionLifecycle{}).CompletionEligible(ctx, memberpostgres.Bind(db), memberID)
}

func (MemberDeletion) AvatarAssetID(ctx context.Context, db *gorm.DB, memberID string) (string, error) {
	return (member.DeletionLifecycle{}).AvatarAssetID(ctx, memberpostgres.Bind(db), memberID)
}

func (MemberDeletion) ListExpiredUnonboarded(
	ctx context.Context,
	db *gorm.DB,
	cutoff time.Time,
	limit int,
	after *account.MemberUnonboardedCursor,
) ([]account.MemberUnonboardedCandidate, *account.MemberUnonboardedCursor, error) {
	var memberCursor *member.UnonboardedCursor
	if after != nil {
		memberCursor = &member.UnonboardedCursor{CreatedAt: after.CreatedAt, MemberID: after.MemberID}
	}
	rows, next, err := (member.DeletionLifecycle{}).ListExpiredUnonboarded(ctx, memberpostgres.Bind(db), cutoff, limit, memberCursor)
	result := make([]account.MemberUnonboardedCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, account.MemberUnonboardedCandidate{
			MemberID: row.MemberID, IdentityID: row.IdentityID, CreatedAt: row.CreatedAt,
		})
	}
	if next == nil {
		return result, nil, err
	}
	return result, &account.MemberUnonboardedCursor{CreatedAt: next.CreatedAt, MemberID: next.MemberID}, err
}

func (MemberDeletion) RecheckUnonboarded(
	ctx context.Context, tx *gorm.DB, memberID, identityID string, cutoff *time.Time,
) (bool, error) {
	return (member.DeletionLifecycle{}).RecheckUnonboarded(ctx, memberpostgres.Bind(tx), memberID, identityID, cutoff)
}

func (MemberDeletion) UnonboardedIdentity(ctx context.Context, db *gorm.DB, memberID string) (string, bool, error) {
	return (member.DeletionLifecycle{}).UnonboardedIdentity(ctx, memberpostgres.Bind(db), memberID)
}
