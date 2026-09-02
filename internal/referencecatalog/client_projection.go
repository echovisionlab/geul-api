package referencecatalog

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// =============================================================================
// Helper Methods
// =============================================================================

type clientLogoAssets struct {
	Light *commonv1.AssetRef
	Dark  *commonv1.AssetRef
}

func optionalLogoResponseAsset(requested, target managev1.ThemeAssetVariant, asset *commonv1.AssetRef) *commonv1.AssetRef {
	if requested == target || (requested == managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED && target == managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT) {
		return asset
	}
	return nil
}

func (s *ClientService) getClientLogoAssets(ctx context.Context, clientID string) *clientLogoAssets {
	var row struct {
		LightFileID *string `gorm:"column:logo_light_file_id"`
		DarkFileID  *string `gorm:"column:logo_dark_file_id"`
	}
	err := s.db.WithContext(ctx).Table("client").
		Select("logo_light_file_id, logo_dark_file_id").
		Where("id = ?", clientID).
		Take(&row).Error
	if err != nil {
		return nil
	}

	assets := &clientLogoAssets{}
	resolve := func(fileID *string) *commonv1.AssetRef {
		if fileID == nil || strings.TrimSpace(*fileID) == "" {
			return nil
		}
		asset, err := s.assets.ReadyRef(ctx, s.db, AssetSource{FileID: *fileID, Kind: "logo"})
		if err != nil {
			return nil
		}
		return asset
	}
	assets.Light = resolve(row.LightFileID)
	assets.Dark = resolve(row.DarkFileID)
	return assets
}

// toProtoClient converts a model.Client to protobuf Client.
func (s *ClientService) toProtoClient(c *model.Client, logoAssets *clientLogoAssets) *managev1.Client {
	client := &managev1.Client{
		Id:        c.ID,
		Name:      c.Name,
		CreatedAt: timestamppb.New(c.CreatedAt),
	}

	if c.Website != nil {
		client.Website = c.Website
	}
	if logoAssets != nil {
		client.LogoLightAsset = logoAssets.Light
		client.LogoDarkAsset = logoAssets.Dark
	}

	return client
}
