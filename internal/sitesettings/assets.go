package sitesettings

import (
	"context"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func siteSettingAssetBinding(settings *model.SiteSettings, key string) (*string, string, string, bool) {
	switch key {
	case "logo_light_file_id":
		return settings.LogoLightFileID, "logo:light", "logo", true
	case "logo_dark_file_id":
		return settings.LogoDarkFileID, "logo:dark", "logo", true
	case "logo_email_file_id":
		return settings.LogoEmailFileID, "logo:email", "email_image", true
	case "favicon_file_id":
		return settings.FaviconFileID, "favicon", "favicon", true
	case "site_og_background_file_id":
		return settings.SiteOgBackgroundFileID, "og_background:site", "image", true
	case "privacy_og_background_file_id":
		return settings.PrivacyOgBackgroundFileID, "og_background:privacy", "image", true
	case "terms_og_background_file_id":
		return settings.TermsOgBackgroundFileID, "og_background:terms", "image", true
	default:
		return nil, "", "", false
	}
}

func (s *SiteSettingService) syncSiteSettingAssetBinding(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	key string,
) error {
	fileID, bindingKey, expectedKind, managed := siteSettingAssetBinding(settings, key)
	if !managed {
		return nil
	}
	if key == "favicon_file_id" {
		return s.assets.ReplaceFavicon(ctx, tx, fileID)
	}
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return s.assets.Release(ctx, tx, bindingKey)
	}
	_, err := s.assets.BindReady(ctx, tx, AssetBinding{
		SourceFileID: *fileID,
		Key:          bindingKey,
		Kind:         expectedKind,
	})
	return err
}

func (s *SiteSettingService) getFileAsset(ctx context.Context, fileID string, expectedKind string) *commonv1.AssetRef {
	if fileID == "" {
		return nil
	}
	asset, err := s.assets.ReadyRef(ctx, s.db, fileID, expectedKind)
	if err != nil {
		slog.Warn("ready asset referenced in site setting not found", "file_id", fileID, "error", err)
		return nil
	}
	return asset
}

type siteLoaderAssetRow struct {
	FileID string `gorm:"column:file_id"`
}

func (s *SiteSettingService) loadSiteLoaderAssets(ctx context.Context, db *gorm.DB) ([]*managev1.SiteLoaderAsset, error) {
	var rows []siteLoaderAssetRow
	if err := db.WithContext(ctx).
		Table("site_setting_loader_file AS slf").
		Select("slf.file_id").
		Where("slf.site_setting_id = ?", 1).
		Order("slf.position ASC, slf.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	assets := make([]*managev1.SiteLoaderAsset, 0, len(rows))
	for _, row := range rows {
		ref, err := s.assets.ReadyRef(ctx, db, row.FileID, "loader")
		if err != nil {
			return nil, err
		}
		assets = append(assets, &managev1.SiteLoaderAsset{
			FileId: row.FileID,
			Asset:  ref,
		})
	}

	return assets, nil
}

func applyLoaderAssetsToPublicSettings(public *managev1.PublicSettings, assets []*managev1.SiteLoaderAsset) {
	public.LoaderAssets = make([]*managev1.SiteLoaderAsset, 0, len(assets))
	for _, asset := range assets {
		if asset == nil || asset.Asset == nil {
			continue
		}
		public.LoaderAssets = append(public.LoaderAssets, asset)
	}
}
