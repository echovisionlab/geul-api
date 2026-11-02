package sitesettings

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetSettingQuery reads one setting by key.
type GetSettingQuery struct {
	Key string
}

// LoaderAsset is a site loader File and its ready asset.
type LoaderAsset struct {
	FileID string
	Asset  Asset
}

// AdminSettings is what an administrator reads: the stored settings, with
// an empty reference read as unset, the ready asset of each File setting
// (nil when it has none), the decoded JSON settings and the runtime origin.
type AdminSettings struct {
	SiteTitle              string
	CompanyName            string
	CompanyAddress         string
	TaxID                  string
	LegalEmail             string
	ContactEmail           string
	PrimaryColor           string
	DefaultCommentsEnabled bool
	DefaultMapThemeID      string
	MetaDescription        string

	HomepagePageID       *string
	MenuHeaderID         *string
	MenuSecondaryID      *string
	MenuFooterID         *string
	MenuAvatarDropdownID *string
	GoogleAnalyticsID    *string

	LogoFullLight       *Asset
	LogoFullDark        *Asset
	LogoSymbolLight     *Asset
	LogoSymbolDark      *Asset
	LogoEmailLight      *Asset
	LogoEmailDark       *Asset
	Favicon             *Asset
	FaviconSet          *FaviconSet
	SiteOgBackground    *Asset
	PrivacyOgBackground *Asset
	TermsOgBackground   *Asset
	LoaderAssets        []LoaderAsset

	// SocialLinks is nil when no link is stored or the stored value is
	// undecodable; OGImageConfig is nil when it is unset, empty or
	// undecodable.
	SocialLinks   structured.Fields
	OGImageConfig structured.Fields

	SiteOrigin string
}

func (s *Service) requireView(ctx context.Context) error {
	can, err := policyv1.SiteSetting.View(siteSettingAuthorizationID)
	if err != nil {
		return err
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// GetSettings returns all settings including sensitive ones (admin only).
func (s *Service) GetSettings(ctx context.Context) (AdminSettings, error) {
	if err := s.requireView(ctx); err != nil {
		return AdminSettings{}, err
	}
	var settings Settings
	var loaders []LoaderAsset
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		if settings, err = store.Settings(ctx); err != nil {
			return err
		}
		loaders, err = loaderAssets(ctx, store)
		return err
	})
	if err != nil {
		return AdminSettings{}, err
	}
	all := AdminSettings{
		SiteTitle:              settings.SiteTitle,
		CompanyName:            settings.CompanyName,
		CompanyAddress:         settings.CompanyAddress,
		TaxID:                  settings.TaxID,
		LegalEmail:             settings.LegalEmail,
		ContactEmail:           settings.ContactEmail,
		PrimaryColor:           settings.PrimaryColor,
		DefaultCommentsEnabled: settings.DefaultCommentsEnabled,
		DefaultMapThemeID:      settings.DefaultMapThemeID,
		MetaDescription:        settings.MetaDescription,
		HomepagePageID:         nonEmpty(settings.HomepagePageID),
		MenuHeaderID:           nonEmpty(settings.MenuHeaderID),
		MenuSecondaryID:        nonEmpty(settings.MenuSecondaryID),
		MenuFooterID:           nonEmpty(settings.MenuFooterID),
		MenuAvatarDropdownID:   nonEmpty(settings.MenuAvatarDropdownID),
		GoogleAnalyticsID:      nonEmpty(settings.GoogleAnalyticsID),
		LogoFullLight:          s.optionalFileAsset(ctx, settings.LogoFullLightFileID, "logo"),
		LogoFullDark:           s.optionalFileAsset(ctx, settings.LogoFullDarkFileID, "logo"),
		LogoSymbolLight:        s.optionalFileAsset(ctx, settings.LogoSymbolLightFileID, "logo"),
		LogoSymbolDark:         s.optionalFileAsset(ctx, settings.LogoSymbolDarkFileID, "logo"),
		LogoEmailLight:         s.optionalFileAsset(ctx, settings.LogoEmailLightFileID, "email_image"),
		LogoEmailDark:          s.optionalFileAsset(ctx, settings.LogoEmailDarkFileID, "email_image"),
		LoaderAssets:           loaders,
		SiteOrigin:             s.siteOrigin,
	}
	if settings.FaviconFileID != nil {
		all.Favicon, all.FaviconSet = s.assets.Favicon(ctx, *settings.FaviconFileID)
	}
	all.SiteOgBackground = s.optionalFileAsset(ctx, settings.SiteOgBackgroundFileID, "image")
	all.PrivacyOgBackground = s.optionalFileAsset(ctx, settings.PrivacyOgBackgroundFileID, "image")
	all.TermsOgBackground = s.optionalFileAsset(ctx, settings.TermsOgBackgroundFileID, "image")

	socialLinks, err := jsonColumnToMap(settings.SocialLinks, true)
	if err != nil {
		logInvalidStoredSetting(ctx, "social_links", err)
	}
	if len(socialLinks) > 0 {
		all.SocialLinks = socialLinks
	}
	ogImageConfig, err := jsonColumnToMap(settings.OGImageConfig, false)
	if err != nil {
		logInvalidStoredSetting(ctx, "og_image_config", err)
	}
	if len(ogImageConfig) > 0 {
		all.OGImageConfig = ogImageConfig
	}
	return all, nil
}

// loaderAssets reads the loader Files that have a ready asset, in order.
func loaderAssets(ctx context.Context, store Store) ([]LoaderAsset, error) {
	fileIDs, err := store.LoaderFileIDs(ctx)
	if err != nil {
		return nil, err
	}
	assets := make([]LoaderAsset, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		asset, err := store.ReadyAsset(ctx, fileID, "loader")
		if err != nil {
			return nil, err
		}
		if asset != nil {
			assets = append(assets, LoaderAsset{FileID: fileID, Asset: *asset})
		}
	}
	return assets, nil
}

// optionalFileAsset logs a failed lookup and serves the setting without the asset.
func (s *Service) optionalFileAsset(ctx context.Context, fileID *string, expectedKind string) *Asset {
	if fileID == nil || *fileID == "" {
		return nil
	}
	asset, err := s.assets.ReadyAsset(ctx, *fileID, expectedKind)
	if err != nil {
		slog.WarnContext(ctx, "Site setting asset lookup failed; serving the setting without it", "domain", "sitesettings", "operation", "resolve_setting_asset", "outcome", "failed", "file_id", *fileID, failurecause.Attr(err))
		return nil
	}
	return asset
}

// nonEmpty returns a copy of a set, non-empty value.
func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	copied := *value
	return &copied
}

// GetSetting retrieves a single setting by key.
func (s *Service) GetSetting(ctx context.Context, query GetSettingQuery) (Setting, error) {
	if err := s.requireView(ctx); err != nil {
		return Setting{}, err
	}
	var settings Settings
	if err := s.units.Read(ctx, func(store Store) (err error) {
		settings, err = store.Settings(ctx)
		return err
	}); err != nil {
		return Setting{}, err
	}
	value, ok, decodeErr := decodedSettingValue(&settings, query.Key)
	if !ok {
		return Setting{}, domainerr.InvalidField("key", fmt.Sprintf("invalid setting key: %s", query.Key))
	}
	if decodeErr != nil {
		logInvalidStoredSetting(ctx, query.Key, decodeErr)
	}
	return Setting{Key: query.Key, Value: value}, nil
}

// logInvalidStoredSetting records a stored setting that a read replaced by its
// empty fallback. The read still succeeds, so no request record holds it.
func logInvalidStoredSetting(ctx context.Context, key string, err error) {
	slog.ErrorContext(ctx, "Stored site setting is invalid; serving it empty", "domain", "sitesettings", "operation", "decode_stored_setting", "outcome", "failed", "key", key, failurecause.Attr(err))
}
