//go:build integration

package account

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/member"
	"gorm.io/gorm"
)

// memberDeletionIntegrationAdapter keeps same-package Account integration
// tests at the real Member boundary without importing the composition adapter
// back into the Account package under test.
type memberDeletionIntegrationAdapter struct{}

var _ MemberDeletionLifecycle = memberDeletionIntegrationAdapter{}

func (memberDeletionIntegrationAdapter) PrepareTombstone(ctx context.Context, db *gorm.DB, memberID, identityID, email string) (MemberDeletionSnapshot, error) {
	snapshot, err := (member.DeletionLifecycle{}).PrepareTombstone(ctx, db, memberID, identityID, email)
	return MemberDeletionSnapshot{MemberID: snapshot.MemberID, IdentityID: snapshot.IdentityID, PrimaryEmailSnapshot: snapshot.PrimaryEmailSnapshot, AlreadyTombstoned: snapshot.AlreadyTombstoned, IdentityLinked: snapshot.IdentityLinked}, err
}

func (memberDeletionIntegrationAdapter) FinalizeTombstone(ctx context.Context, db *gorm.DB, snapshot MemberDeletionSnapshot, deletedAt time.Time, appendAudit MemberDeletionAudit) error {
	return (member.DeletionLifecycle{}).FinalizeTombstone(ctx, db, member.DeletionSnapshot{MemberID: snapshot.MemberID, IdentityID: snapshot.IdentityID, PrimaryEmailSnapshot: snapshot.PrimaryEmailSnapshot, AlreadyTombstoned: snapshot.AlreadyTombstoned, IdentityLinked: snapshot.IdentityLinked}, deletedAt, member.DeletionAudit(appendAudit))
}

func (memberDeletionIntegrationAdapter) PrepareUnonboardedHardDelete(ctx context.Context, db *gorm.DB, memberID, identityID string) (MemberUnonboardedDeletionTarget, error) {
	target, err := (member.DeletionLifecycle{}).PrepareUnonboardedHardDelete(ctx, db, memberID, identityID)
	return MemberUnonboardedDeletionTarget{MemberID: target.MemberID, IdentityID: target.IdentityID, IdentityLinked: target.IdentityLinked}, err
}

func (memberDeletionIntegrationAdapter) FinalizeUnonboardedHardDelete(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, target MemberUnonboardedDeletionTarget, appendAudit MemberDeletionAudit) error {
	return (member.DeletionLifecycle{}).FinalizeUnonboardedHardDelete(ctx, db, spicedb, member.UnonboardedDeletionTarget{MemberID: target.MemberID, IdentityID: target.IdentityID, IdentityLinked: target.IdentityLinked}, member.DeletionAudit(appendAudit))
}

func (memberDeletionIntegrationAdapter) CleanupAvatar(ctx context.Context, db *gorm.DB, memberID, assetID string) error {
	return (member.DeletionLifecycle{}).CleanupAvatar(ctx, db, memberID, assetID)
}

func (memberDeletionIntegrationAdapter) NotificationSnapshot(ctx context.Context, db *gorm.DB, memberID, identityID string) (MemberDeletionNotification, error) {
	snapshot, err := (member.DeletionLifecycle{}).NotificationSnapshot(ctx, db, memberID, identityID)
	return MemberDeletionNotification{Nickname: snapshot.Nickname, PrimaryEmail: snapshot.PrimaryEmail, Locale: snapshot.Locale}, err
}

func (memberDeletionIntegrationAdapter) CompletionEligible(ctx context.Context, db *gorm.DB, memberID string) (bool, error) {
	return (member.DeletionLifecycle{}).CompletionEligible(ctx, db, memberID)
}

func (memberDeletionIntegrationAdapter) AvatarAssetID(ctx context.Context, db *gorm.DB, memberID string) (string, error) {
	return (member.DeletionLifecycle{}).AvatarAssetID(ctx, db, memberID)
}

func (memberDeletionIntegrationAdapter) ListExpiredUnonboarded(ctx context.Context, db *gorm.DB, cutoff time.Time, limit int, after *MemberUnonboardedCursor) ([]MemberUnonboardedCandidate, *MemberUnonboardedCursor, error) {
	var cursor *member.UnonboardedCursor
	if after != nil {
		cursor = &member.UnonboardedCursor{CreatedAt: after.CreatedAt, MemberID: after.MemberID}
	}
	rows, next, err := (member.DeletionLifecycle{}).ListExpiredUnonboarded(ctx, db, cutoff, limit, cursor)
	result := make([]MemberUnonboardedCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, MemberUnonboardedCandidate{MemberID: row.MemberID, IdentityID: row.IdentityID, CreatedAt: row.CreatedAt})
	}
	if next == nil {
		return result, nil, err
	}
	return result, &MemberUnonboardedCursor{CreatedAt: next.CreatedAt, MemberID: next.MemberID}, err
}

func (memberDeletionIntegrationAdapter) RecheckUnonboarded(ctx context.Context, tx *gorm.DB, memberID, identityID string, cutoff *time.Time) (bool, error) {
	return (member.DeletionLifecycle{}).RecheckUnonboarded(ctx, tx, memberID, identityID, cutoff)
}

func (memberDeletionIntegrationAdapter) UnonboardedIdentity(ctx context.Context, db *gorm.DB, memberID string) (string, bool, error) {
	return (member.DeletionLifecycle{}).UnonboardedIdentity(ctx, db, memberID)
}
