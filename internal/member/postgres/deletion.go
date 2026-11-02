package memberpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func (s store) DeletionMember(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Select("id", "account_identity_id", "primary_email", "available_emails", "onboarded", "deleted_at").Where("id = ?::uuid", memberID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) DeletionPairWitness(ctx context.Context, memberID, identityID string) (bool, bool, error) {
	var state struct {
		Linked         bool `gorm:"column:linked"`
		IdentityExists bool `gorm:"column:identity_exists"`
	}
	err := s.db(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM member `+identityread.JoinLinked("member", "identity")+` WHERE member.id = ?::uuid AND identity.id = ?::uuid) AS linked, `+identityread.ExistsSQL+` AS identity_exists`, memberID, identityID, identityID).Scan(&state).Error
	return state.Linked, state.IdentityExists, err
}

func (s store) IdentityExists(ctx context.Context, identityID string) (bool, error) {
	return identityread.Exists(ctx, s.tx, identityID)
}

func (s store) LockMember(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id = ?::uuid", memberID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) DeletePersonalAccessTokens(ctx context.Context, memberID string) error {
	return s.db(ctx).Exec(`DELETE FROM member_personal_access_token WHERE member_id = ?::uuid`, memberID).Error
}

func (s store) TombstoneMember(ctx context.Context, memberID string, deletedAt time.Time) error {
	return s.db(ctx).Model(&model.Member{}).Where("id = ?::uuid AND account_identity_id IS NULL AND deleted_at IS NULL", memberID).Updates(structured.Fields{"bio": nil, "website": nil, "social_links": gorm.Expr(`'{}'::jsonb`), "preferred_locale": nil, "deleted_at": deletedAt, "updated_at": deletedAt}).Error
}

func (s store) NotificationMember(ctx context.Context, memberID, identityID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Select("nickname", "primary_email", "preferred_locale").Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, identityID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) CompletionEligible(ctx context.Context, memberID string) (bool, error) {
	var found bool
	if err := s.db(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid AND deleted_at IS NOT NULL AND account_identity_id IS NULL AND onboarded = TRUE)`, memberID).Scan(&found).Error; err != nil {
		return false, err
	}
	return found, nil
}

func (s store) AvatarAssetID(ctx context.Context, memberID string) (string, error) {
	var assetID string
	err := s.db(ctx).Table("public_asset_binding").Select("asset_id").Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "member", memberID, "avatar").Take(&assetID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return strings.TrimSpace(assetID), err
}

func (s store) ReleaseAvatarBindings(ctx context.Context, memberID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), "").ReleasePublicAssetBindings(ctx, "member", memberID, "avatar")
}

func (s store) PublicAssetStatus(ctx context.Context, assetID string) (string, bool, error) {
	var asset model.PublicAsset
	if err := s.db(ctx).Select("id", "status").Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return asset.Status, true, nil
}

func (s store) RequestPublicAssetDeletion(ctx context.Context, assetID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), "").RequestPublicAssetDeletion(ctx, assetID)
}

func (s store) DeleteUnonboardedReferences(ctx context.Context, memberID string) error {
	if err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), "").ReleasePublicAssetBindings(ctx, "member", memberID, "avatar"); err != nil {
		return fmt.Errorf("release unonboarded Member avatar: %w", err)
	}
	if err := s.db(ctx).Where("member_id = ?::uuid", memberID).Delete(&model.UserTagMapping{}).Error; err != nil {
		return fmt.Errorf("delete unonboarded Member tags: %w", err)
	}
	if err := s.db(ctx).Exec(`UPDATE auth_bootstrap_state SET member_id = NULL WHERE key = 'first_admin' AND member_id = ?::uuid`, memberID).Error; err != nil {
		return fmt.Errorf("retain first-admin bootstrap claim: %w", err)
	}
	if err := s.db(ctx).Exec(`DELETE FROM auth_bootstrap_state WHERE key <> 'first_admin' AND member_id = ?::uuid`, memberID).Error; err != nil {
		return fmt.Errorf("delete unonboarded Member bootstrap reference: %w", err)
	}
	return nil
}

func (s store) DeleteUnonboardedMember(ctx context.Context, memberID string) (bool, error) {
	result := s.db(ctx).Where("id = ?::uuid AND onboarded = FALSE AND deleted_at IS NULL AND account_identity_id IS NULL", memberID).Delete(&model.Member{})
	return result.RowsAffected != 0, result.Error
}

func (s store) ExpiredUnonboarded(ctx context.Context, cutoff time.Time, limit int, after *member.UnonboardedCursor) ([]member.UnonboardedCandidate, error) {
	var rows []struct {
		MemberID   string    `gorm:"column:member_id"`
		IdentityID string    `gorm:"column:identity_id"`
		CreatedAt  time.Time `gorm:"column:created_at"`
	}
	query := s.db(ctx).Table("member AS member").Joins(identityread.JoinLinked("member", "identity")).Select("member.id::text AS member_id", "identity.id::text AS identity_id", "member.created_at").Where("member.onboarded = FALSE AND member.deleted_at IS NULL AND member.created_at <= ?", cutoff)
	if after != nil {
		query = query.Where("(member.created_at, member.id) > (?, ?::uuid)", after.CreatedAt, after.MemberID)
	}
	if err := query.Order("member.created_at ASC, member.id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]member.UnonboardedCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, member.UnonboardedCandidate{MemberID: row.MemberID, IdentityID: row.IdentityID, CreatedAt: row.CreatedAt})
	}
	return result, nil
}

func (s store) UnonboardedLinked(ctx context.Context, memberID, identityID string, cutoff *time.Time) (bool, error) {
	query := s.db(ctx).Table("member AS member").Joins(identityread.JoinLinked("member", "identity")).Select("member.id").Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.onboarded = FALSE AND member.deleted_at IS NULL", memberID, identityID)
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

func (s store) UnonboardedIdentity(ctx context.Context, memberID string) (string, bool, error) {
	var identityID string
	err := s.db(ctx).Table("member AS member").Joins(identityread.JoinLinked("member", "identity")).Select("identity.id::text").Where("member.id = ?::uuid AND member.onboarded = FALSE AND member.deleted_at IS NULL", memberID).Take(&identityID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(identityID), identityID != "", nil
}
