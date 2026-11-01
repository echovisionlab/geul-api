package sitesettingspostgres

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// SettingsFromRow maps the settings row to the domain's settings.
func SettingsFromRow(row model.SiteSettings) sitesettings.Settings {
	return sitesettings.Settings{
		SiteTitle:                 row.SiteTitle,
		CompanyName:               row.CompanyName,
		CompanyAddress:            row.CompanyAddress,
		TaxID:                     row.TaxID,
		ContactEmail:              row.ContactEmail,
		LegalEmail:                row.LegalEmail,
		SocialLinks:               row.SocialLinks,
		LogoFullLightFileID:       row.LogoFullLightFileID,
		LogoFullDarkFileID:        row.LogoFullDarkFileID,
		LogoSymbolLightFileID:     row.LogoSymbolLightFileID,
		LogoSymbolDarkFileID:      row.LogoSymbolDarkFileID,
		LogoEmailLightFileID:      row.LogoEmailLightFileID,
		LogoEmailDarkFileID:       row.LogoEmailDarkFileID,
		FaviconFileID:             row.FaviconFileID,
		PrimaryColor:              row.PrimaryColor,
		SiteOgBackgroundFileID:    row.SiteOgBackgroundFileID,
		PrivacyOgBackgroundFileID: row.PrivacyOgBackgroundFileID,
		TermsOgBackgroundFileID:   row.TermsOgBackgroundFileID,
		DefaultCommentsEnabled:    row.DefaultCommentsEnabled,
		HomepagePageID:            row.HomepagePageID,
		DefaultMapThemeID:         row.DefaultMapThemeID,
		MenuHeaderID:              row.MenuHeaderID,
		MenuSecondaryID:           row.MenuSecondaryID,
		MenuFooterID:              row.MenuFooterID,
		MenuAvatarDropdownID:      row.MenuAvatarDropdownID,
		MetaDescription:           row.MetaDescription,
		GoogleAnalyticsID:         row.GoogleAnalyticsID,
		SiteOgAssetID:             row.SiteOgAssetID,
		OGImageConfig:             row.OGImageConfig,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
}

func rowFromSettings(settings sitesettings.Settings) model.SiteSettings {
	return model.SiteSettings{
		ID:                        1,
		SiteTitle:                 settings.SiteTitle,
		CompanyName:               settings.CompanyName,
		CompanyAddress:            settings.CompanyAddress,
		TaxID:                     settings.TaxID,
		ContactEmail:              settings.ContactEmail,
		LegalEmail:                settings.LegalEmail,
		SocialLinks:               settings.SocialLinks,
		LogoFullLightFileID:       settings.LogoFullLightFileID,
		LogoFullDarkFileID:        settings.LogoFullDarkFileID,
		LogoSymbolLightFileID:     settings.LogoSymbolLightFileID,
		LogoSymbolDarkFileID:      settings.LogoSymbolDarkFileID,
		LogoEmailLightFileID:      settings.LogoEmailLightFileID,
		LogoEmailDarkFileID:       settings.LogoEmailDarkFileID,
		FaviconFileID:             settings.FaviconFileID,
		PrimaryColor:              settings.PrimaryColor,
		SiteOgBackgroundFileID:    settings.SiteOgBackgroundFileID,
		PrivacyOgBackgroundFileID: settings.PrivacyOgBackgroundFileID,
		TermsOgBackgroundFileID:   settings.TermsOgBackgroundFileID,
		DefaultCommentsEnabled:    settings.DefaultCommentsEnabled,
		HomepagePageID:            settings.HomepagePageID,
		DefaultMapThemeID:         settings.DefaultMapThemeID,
		MenuHeaderID:              settings.MenuHeaderID,
		MenuSecondaryID:           settings.MenuSecondaryID,
		MenuFooterID:              settings.MenuFooterID,
		MenuAvatarDropdownID:      settings.MenuAvatarDropdownID,
		MetaDescription:           settings.MetaDescription,
		GoogleAnalyticsID:         settings.GoogleAnalyticsID,
		SiteOgAssetID:             settings.SiteOgAssetID,
		OGImageConfig:             settings.OGImageConfig,
		CreatedAt:                 settings.CreatedAt,
		UpdatedAt:                 settings.UpdatedAt,
	}
}

// AssetFromRef maps a ready asset the media runtime projects; nil stays nil.
func AssetFromRef(ref *commonv1.AssetRef) *sitesettings.Asset {
	if ref == nil {
		return nil
	}
	return &sitesettings.Asset{
		ID:               ref.GetAssetId(),
		URL:              ref.GetUrl(),
		Extension:        ref.GetExtension(),
		MIMEType:         ref.GetMimeType(),
		FileSize:         ref.GetFileSize(),
		SHA256:           ref.GetSha256(),
		Disposition:      sitesettings.AssetDisposition(ref.GetDisposition()),
		DownloadFilename: ref.DownloadFilename,
	}
}

// FaviconSetFromRef maps a generated favicon set; nil stays nil.
func FaviconSetFromRef(set *commonv1.FaviconAssetSet) *sitesettings.FaviconSet {
	if set == nil {
		return nil
	}
	return &sitesettings.FaviconSet{
		ICO:           AssetFromRef(set.GetIconIco()),
		PNG16:         AssetFromRef(set.GetIconPng_16()),
		PNG32:         AssetFromRef(set.GetIconPng_32()),
		PNG48:         AssetFromRef(set.GetIconPng_48()),
		AppleTouch180: AssetFromRef(set.GetAppleTouchIcon_180()),
		Manifest192:   AssetFromRef(set.GetManifestIcon_192()),
		Manifest512:   AssetFromRef(set.GetManifestIcon_512()),
		SVG:           AssetFromRef(set.GetIconSvg()),
	}
}
