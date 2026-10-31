package sitesettings

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
)

func validateSiteSettingKeys(settings []Setting) error {
	invalidKeys := make([]string, 0)
	for _, setting := range settings {
		if _, ok := defaultSettings[setting.Key]; !ok {
			invalidKeys = append(invalidKeys, setting.Key)
		}
	}
	if len(invalidKeys) == 0 {
		return nil
	}
	return domainerr.InvalidField("keys", fmt.Sprintf("invalid setting keys: %v", invalidKeys))
}

func cloneSiteSettingsForComparison(settings *model.SiteSettings) model.SiteSettings {
	clone := *settings
	clone.OGImageConfig = append([]byte(nil), settings.OGImageConfig...)
	return clone
}

func applyRequestedSiteSettings(settings *model.SiteSettings, requested []Setting) error {
	if err := validateSiteSettingKeys(requested); err != nil {
		return err
	}
	for _, setting := range requested {
		if err := applySettingValue(settings, setting.Key, setting.Value); err != nil {
			return domainerr.InvalidField(
				"value",
				fmt.Sprintf("invalid value for key %s: %v", setting.Key, err),
			)
		}
	}
	return nil
}

func (s *Service) validateSiteSettingReferences(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	keys []string,
) error {
	for _, key := range keys {
		if key == "homepage_page_id" || isSiteSettingMenuReference(key) {
			if err := s.references.Validate(ctx, tx, key, siteSettingReferenceValue(settings, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) lockAndValidateSiteSettingAssets(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	keys []string,
) error {
	fileIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		fileID, _, _, managed := siteSettingAssetBinding(settings, key)
		if managed && fileID != nil {
			fileIDs = append(fileIDs, *fileID)
		}
	}
	if err := s.assets.LockForAttachment(ctx, tx, fileIDs); err != nil {
		return err
	}
	validatedKeys := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, exists := validatedKeys[key]; exists {
			continue
		}
		validatedKeys[key] = struct{}{}
		fileID, _, _, managed := siteSettingAssetBinding(settings, key)
		if managed && fileID != nil {
			if err := s.assets.ValidateAttachment(ctx, tx, key, *fileID); err != nil {
				return err
			}
		}
	}
	return nil
}
