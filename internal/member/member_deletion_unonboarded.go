package member

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (DeletionLifecycle) PrepareUnonboardedHardDelete(
	ctx context.Context,
	db *gorm.DB,
	memberID,
	identityID string,
) (UnonboardedDeletionTarget, error) {
	target := UnonboardedDeletionTarget{MemberID: memberID, IdentityID: identityID}
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return target, err
	}
	var member model.Member
	err := db.WithContext(ctx).Select("id", "account_identity_id", "onboarded", "deleted_at").Where("id = ?::uuid", memberID).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UnonboardedDeletionTarget{}, nil
	}
	if err != nil {
		return target, err
	}
	if member.Onboarded || member.DeletedAt != nil {
		return UnonboardedDeletionTarget{}, nil
	}
	target.IdentityLinked = member.AccountIdentityID != nil
	if target.IdentityLinked && strings.TrimSpace(*member.AccountIdentityID) != identityID {
		return target, fmt.Errorf("unonboarded hard deletion identity does not match Member link")
	}
	if target.IdentityLinked {
		linked, identityExists, err := deletionPairWitness(ctx, db, memberID, identityID)
		if err != nil {
			return target, err
		}
		if identityExists && !linked {
			return target, fmt.Errorf("unonboarded Member and identity link is not bilateral")
		}
	}
	return target, nil
}

func (DeletionLifecycle) FinalizeUnonboardedHardDelete(
	ctx context.Context,
	db *gorm.DB,
	spicedb *auth.SpiceDBClient,
	target UnonboardedDeletionTarget,
	appendAudit DeletionAudit,
) error {
	if target.MemberID == "" {
		return nil
	}
	memberSnapshotPlan, err := policyv1.Member.Snapshot(target.MemberID)
	if err != nil {
		return err
	}
	snapshots, readAt, err := spicedb.SnapshotResourceRelationshipDescriptors(ctx, memberSnapshotPlan)
	if err != nil {
		return fmt.Errorf("snapshot unonboarded Member SpiceDB relationships: %w", err)
	}
	deleteRelationships, err := memberAuthorizationSnapshotDeletions(target.MemberID, snapshots)
	if err != nil {
		return err
	}
	deletedAt := readAt
	if len(deleteRelationships) != 0 {
		deletedAt, err = spicedb.ApplyRelationships(ctx, deleteRelationships...)
		if err != nil {
			return fmt.Errorf("delete unonboarded Member SpiceDB relationships: %w", err)
		}
	}
	if err := spicedb.VerifyResourceRelationshipsDeleted(ctx, memberSnapshotPlan, deletedAt); err != nil {
		return fmt.Errorf("prove unonboarded Member SpiceDB relationship deletion: %w", err)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Member
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?::uuid", target.MemberID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if current.Onboarded || current.DeletedAt != nil {
			return nil
		}
		if current.AccountIdentityID != nil {
			if strings.TrimSpace(*current.AccountIdentityID) != target.IdentityID {
				return fmt.Errorf("unonboarded hard deletion identity changed during cleanup")
			}
			return fmt.Errorf("unonboarded Member identity anchor still exists after cleanup")
		}
		if err := deleteUnonboardedReferences(ctx, tx, target.MemberID); err != nil {
			return err
		}
		result := tx.Where("id = ?::uuid AND onboarded = FALSE AND deleted_at IS NULL AND account_identity_id IS NULL", target.MemberID).Delete(&model.Member{})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if appendAudit == nil {
			return nil
		}
		return appendAudit(ctx, tx, target.MemberID)
	})
}

func memberAuthorizationSnapshotDeletions(
	memberID string,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, error) {
	deletePolicy, err := policyv1.Member.DeletePolicy(memberID)
	if err != nil {
		return nil, err
	}
	deletes := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for index, snapshot := range snapshots {
		if !snapshot.Matches(deletePolicy) {
			return nil, fmt.Errorf("member relationship snapshot %d is outside the Member contract", index)
		}
		deletes = append(deletes, deletePolicy)
	}
	return deletes, nil
}

func (DeletionLifecycle) ListExpiredUnonboarded(
	ctx context.Context,
	db *gorm.DB,
	cutoff time.Time,
	limit int,
	after *UnonboardedCursor,
) ([]UnonboardedCandidate, *UnonboardedCursor, error) {
	if limit <= 0 {
		return []UnonboardedCandidate{}, nil, nil
	}
	var rows []struct {
		MemberID   string    `gorm:"column:member_id"`
		IdentityID string    `gorm:"column:identity_id"`
		CreatedAt  time.Time `gorm:"column:created_at"`
	}
	query := db.WithContext(ctx).Table("member AS member").Joins(`JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text`).Select("member.id::text AS member_id", "identity.id::text AS identity_id", "member.created_at").Where("member.onboarded = FALSE AND member.deleted_at IS NULL AND member.created_at <= ?", cutoff.UTC())
	if after != nil {
		query = query.Where("(member.created_at, member.id) > (?, ?::uuid)", after.CreatedAt, after.MemberID)
	}
	if err := query.Order("member.created_at ASC, member.id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("list expired unonboarded Members: %w", err)
	}
	result := make([]UnonboardedCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, UnonboardedCandidate{MemberID: row.MemberID, IdentityID: row.IdentityID, CreatedAt: row.CreatedAt})
	}
	if len(result) == 0 {
		return result, nil, nil
	}
	last := result[len(result)-1]
	return result, &UnonboardedCursor{CreatedAt: last.CreatedAt, MemberID: last.MemberID}, nil
}

// RecheckUnonboarded reads the Member-owned deletion predicate after Account
// holds the identity mutation fence. It creates no claim, intent, or other
// state: false is the stale-command/admin no-op. A nil cutoff is the
// admin-request path; a non-nil cutoff is the seven-day retention path.
func (DeletionLifecycle) RecheckUnonboarded(
	ctx context.Context,
	tx *gorm.DB,
	memberID,
	identityID string,
	cutoff *time.Time,
) (bool, error) {
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return false, err
	}
	query := tx.WithContext(ctx).Table("member AS member").Joins(`JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text`).Select("member.id").Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.onboarded = FALSE AND member.deleted_at IS NULL", memberID, identityID)
	if cutoff != nil {
		query = query.Where("member.created_at <= ?", cutoff.UTC())
	}
	var found string
	err := query.Take(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (DeletionLifecycle) UnonboardedIdentity(ctx context.Context, db *gorm.DB, memberID string) (string, bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", false, err
	}
	var identityID string
	err := db.WithContext(ctx).Table("member AS member").Joins(`JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text`).Select("identity.id::text").Where("member.id = ?::uuid AND member.onboarded = FALSE AND member.deleted_at IS NULL", memberID).Take(&identityID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(identityID), identityID != "", nil
}

func deletionPairWitness(
	ctx context.Context,
	db *gorm.DB,
	memberID,
	identityID string,
) (linked bool, identityExists bool, err error) {
	var state struct {
		Linked         bool `gorm:"column:linked"`
		IdentityExists bool `gorm:"column:identity_exists"`
	}
	err = db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM member JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text WHERE member.id = ?::uuid AND identity.id = ?::uuid) AS linked, EXISTS (SELECT 1 FROM kratos.identities WHERE id = ?::uuid) AS identity_exists`, memberID, identityID, identityID).Scan(&state).Error
	return state.Linked, state.IdentityExists, err
}

func deleteUnonboardedReferences(ctx context.Context, tx *gorm.DB, memberID string) error {
	if err := mediaasset.NewLifecycle(tx, "").ReleasePublicAssetBindings(ctx, "member", memberID, "avatar"); err != nil {
		return fmt.Errorf("release unonboarded Member avatar: %w", err)
	}
	if err := tx.Where("member_id = ?::uuid", memberID).Delete(&model.UserTagMapping{}).Error; err != nil {
		return fmt.Errorf("delete unonboarded Member tags: %w", err)
	}
	if err := tx.Exec(`UPDATE auth_bootstrap_state SET member_id = NULL WHERE key = 'first_admin' AND member_id = ?::uuid`, memberID).Error; err != nil {
		return fmt.Errorf("retain first-admin bootstrap claim: %w", err)
	}
	if err := tx.Exec(`DELETE FROM auth_bootstrap_state WHERE key <> 'first_admin' AND member_id = ?::uuid`, memberID).Error; err != nil {
		return fmt.Errorf("delete unonboarded Member bootstrap reference: %w", err)
	}
	return nil
}
