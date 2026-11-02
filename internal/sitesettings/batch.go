package sitesettings

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/domainerr"
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

func applyRequestedSiteSettings(settings *Settings, requested []Setting) error {
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

func validateSiteSettingReferences(ctx context.Context, store Store, settings *Settings, keys []string) error {
	for _, key := range keys {
		if key == "homepage_page_id" || isSiteSettingMenuReference(key) {
			if err := store.ValidateReference(ctx, key, siteSettingReferenceValue(settings, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func lockAndValidateSiteSettingAssets(ctx context.Context, store Store, settings *Settings, keys []string) error {
	fileIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		fileID, _, _, managed := siteSettingAssetBinding(settings, key)
		if managed && fileID != nil {
			fileIDs = append(fileIDs, *fileID)
		}
	}
	if err := store.LockFilesForAttachment(ctx, fileIDs); err != nil {
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
			if err := store.ValidateAttachment(ctx, key, *fileID); err != nil {
				return err
			}
		}
	}
	return nil
}
