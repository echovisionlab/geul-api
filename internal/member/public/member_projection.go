package public

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// LoadPublicMemberSummaries resolves a bounded set of Member profiles and their
// current avatar bindings in a fixed number of database queries. Public and
// ordinary domain reads must not call Kratos or load an asset once per row.
func LoadPublicMemberSummaries(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	memberIDs []string,
) (map[string]*commonv1.MemberSummary, error) {
	ids := uniqueNonEmptyIDs(memberIDs)
	result := make(map[string]*commonv1.MemberSummary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	var members []model.Member
	if err := db.WithContext(ctx).
		Where("id IN ? AND onboarded = TRUE", ids).
		Find(&members).Error; err != nil {
		return nil, fmt.Errorf("load members: %w", err)
	}

	avatars, err := loadPublicMemberAvatars(ctx, db, cdnDomain, ids)
	if err != nil {
		return nil, err
	}

	for _, member := range members {
		summary, err := projectPublicMemberSummary(member, avatars[member.ID])
		if err != nil {
			return nil, err
		}
		result[member.ID] = summary
	}

	return result, nil
}

func loadPublicMemberAvatars(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	memberIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	ids := uniqueNonEmptyIDs(memberIDs)
	result := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	type avatarRow struct {
		MemberID string              `gorm:"column:member_id"`
		Asset    readyPublicAssetRow `gorm:"embedded"`
	}
	var avatarRows []avatarRow
	if err := db.WithContext(ctx).
		Table("public_asset_binding AS binding").
		Select("binding.owner_id AS member_id, "+readyPublicAssetSelect("asset")).
		Joins("JOIN public_asset AS asset ON asset.id = binding.asset_id").
		Where("binding.owner_type = ? AND binding.binding_key = ?", "member", "avatar").
		Where("binding.owner_id IN ? AND asset.status = ?", ids, model.PublicAssetStatusReady).
		Find(&avatarRows).Error; err != nil {
		return nil, fmt.Errorf("load member avatars: %w", err)
	}

	for _, row := range avatarRows {
		avatar, err := projectReadyPublicAsset(cdnDomain, row.Asset)
		if err != nil {
			return nil, fmt.Errorf("project member avatar %s: %w", row.MemberID, err)
		}
		result[row.MemberID] = avatar
	}
	return result, nil
}

func projectPublicMemberSummary(member model.Member, avatar *commonv1.AssetRef) (*commonv1.MemberSummary, error) {
	deleted := member.DeletedAt != nil || member.AccountIdentityID == nil
	if deleted {
		avatar = nil
	}
	return &commonv1.MemberSummary{
		Id:          member.ID,
		Nickname:    member.Nickname,
		AvatarAsset: avatar,
		Deleted:     deleted,
	}, nil
}
