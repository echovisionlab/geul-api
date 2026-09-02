package member

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
)

func (DeletionLifecycle) CleanupAvatar(ctx context.Context, db *gorm.DB, memberID, avatarAssetID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if err := mediaasset.NewLifecycle(db, "").ReleasePublicAssetBindings(ctx, "member", memberID, "avatar"); err != nil {
		return fmt.Errorf("release avatar binding: %w", err)
	}
	avatarAssetID = strings.TrimSpace(avatarAssetID)
	if avatarAssetID == "" {
		return nil
	}
	if _, err := uuidutil.ParseCanonical(avatarAssetID, "avatar_asset_id"); err != nil {
		return err
	}
	var asset model.PublicAsset
	if err := db.WithContext(ctx).Select("id", "status").Where("id = ?", avatarAssetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if asset.Status == model.PublicAssetStatusDeletePending || asset.Status == model.PublicAssetStatusDeleted {
		return nil
	}
	if err := mediaasset.NewLifecycle(db, "").RequestPublicAssetDeletion(ctx, asset.ID); err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil
		}
		return err
	}
	return nil
}

func (DeletionLifecycle) AvatarAssetID(ctx context.Context, db *gorm.DB, memberID string) (string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", err
	}
	var assetID string
	err := db.WithContext(ctx).Table("public_asset_binding").Select("asset_id").Where("owner_type = ? AND owner_id = ? AND binding_key = ?", "member", memberID, "avatar").Take(&assetID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return strings.TrimSpace(assetID), err
}
