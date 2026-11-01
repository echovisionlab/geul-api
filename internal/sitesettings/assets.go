package sitesettings

import (
	"context"
	"strings"
)

func siteSettingAssetBinding(settings *Settings, key string) (*string, string, string, bool) {
	switch key {
	case "logo_full_light_file_id":
		return settings.LogoFullLightFileID, "logo_full_light", "logo", true
	case "logo_full_dark_file_id":
		return settings.LogoFullDarkFileID, "logo_full_dark", "logo", true
	case "logo_symbol_light_file_id":
		return settings.LogoSymbolLightFileID, "logo_symbol_light", "logo", true
	case "logo_symbol_dark_file_id":
		return settings.LogoSymbolDarkFileID, "logo_symbol_dark", "logo", true
	case "logo_email_light_file_id":
		return settings.LogoEmailLightFileID, "logo_email_light", "email_image", true
	case "logo_email_dark_file_id":
		return settings.LogoEmailDarkFileID, "logo_email_dark", "email_image", true
	case "favicon_file_id":
		return settings.FaviconFileID, "favicon", "favicon", true
	case "site_og_background_file_id":
		return settings.SiteOgBackgroundFileID, "og_background_site", "image", true
	case "privacy_og_background_file_id":
		return settings.PrivacyOgBackgroundFileID, "og_background_privacy", "image", true
	case "terms_og_background_file_id":
		return settings.TermsOgBackgroundFileID, "og_background_terms", "image", true
	default:
		return nil, "", "", false
	}
}

func syncSiteSettingAssetBinding(ctx context.Context, store Store, settings *Settings, key string) error {
	fileID, bindingKey, expectedKind, managed := siteSettingAssetBinding(settings, key)
	if !managed {
		return nil
	}
	if key == "favicon_file_id" {
		return store.ReplaceFavicon(ctx, fileID)
	}
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return store.ReleaseAssets(ctx, bindingKey)
	}
	return store.BindAsset(ctx, AssetBinding{SourceFileID: *fileID, Key: bindingKey, Kind: expectedKind})
}
