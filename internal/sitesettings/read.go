package sitesettings

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetSettingQuery reads one setting by key.
type GetSettingQuery struct {
	Key string
}

func (s *Service) requireView(ctx context.Context) error {
	can, err := policyv1.SiteSetting.View(siteSettingAuthorizationID)
	if err != nil {
		return err
	}
	return authz.RequireAdminCan(ctx, s.spiceDB, can)
}

// GetSettings returns all settings including sensitive ones (admin only).
func (s *Service) GetSettings(ctx context.Context) (*managev1.AllSettings, error) {
	if err := s.requireView(ctx); err != nil {
		return nil, err
	}
	settings, err := loadSettingsRow(s.db.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	public, err := s.publicSettings(ctx, settings)
	if err != nil {
		return nil, err
	}
	all := &managev1.AllSettings{
		Public:  public,
		Runtime: &managev1.RuntimeSettings{SiteOrigin: s.siteOrigin},
	}
	all.OgImageConfig, err = jsonColumnToStruct(settings.OGImageConfig)
	if err != nil {
		logInvalidStoredSetting(ctx, "og_image_config", err)
	}
	return all, nil
}

func (s *Service) publicSettings(ctx context.Context, settings *model.SiteSettings) (*managev1.PublicSettings, error) {
	public := &managev1.PublicSettings{
		SiteTitle:              settings.SiteTitle,
		CompanyName:            settings.CompanyName,
		CompanyAddress:         settings.CompanyAddress,
		TaxId:                  settings.TaxID,
		LegalEmail:             settings.LegalEmail,
		ContactEmail:           settings.ContactEmail,
		PrimaryColor:           settings.PrimaryColor,
		DefaultCommentsEnabled: settings.DefaultCommentsEnabled,
		DefaultMapThemeId:      settings.DefaultMapThemeID,
		MetaDescription:        settings.MetaDescription,
		HomepagePageId:         nonEmpty(settings.HomepagePageID),
		MenuHeaderId:           nonEmpty(settings.MenuHeaderID),
		MenuSecondaryId:        nonEmpty(settings.MenuSecondaryID),
		MenuFooterId:           nonEmpty(settings.MenuFooterID),
		MenuAvatarDropdownId:   nonEmpty(settings.MenuAvatarDropdownID),
		GoogleAnalyticsId:      nonEmpty(settings.GoogleAnalyticsID),
		LogoFullLightAsset:     s.optionalFileAsset(ctx, settings.LogoFullLightFileID, "logo"),
		LogoFullDarkAsset:      s.optionalFileAsset(ctx, settings.LogoFullDarkFileID, "logo"),
		LogoSymbolLightAsset:   s.optionalFileAsset(ctx, settings.LogoSymbolLightFileID, "logo"),
		LogoSymbolDarkAsset:    s.optionalFileAsset(ctx, settings.LogoSymbolDarkFileID, "logo"),
		LogoEmailLightAsset:    s.optionalFileAsset(ctx, settings.LogoEmailLightFileID, "email_image"),
		LogoEmailDarkAsset:     s.optionalFileAsset(ctx, settings.LogoEmailDarkFileID, "email_image"),
	}
	if settings.FaviconFileID != nil {
		public.FaviconAsset, public.FaviconAssetSet = s.assets.ProjectFavicon(ctx, s.db, *settings.FaviconFileID)
	}
	public.SiteOgBackgroundAsset = s.optionalFileAsset(ctx, settings.SiteOgBackgroundFileID, "image")
	public.PrivacyOgBackgroundAsset = s.optionalFileAsset(ctx, settings.PrivacyOgBackgroundFileID, "image")
	public.TermsOgBackgroundAsset = s.optionalFileAsset(ctx, settings.TermsOgBackgroundFileID, "image")
	loaderAssets, err := s.loadSiteLoaderAssets(ctx, s.db)
	if err != nil {
		return nil, err
	}
	applyLoaderAssetsToPublicSettings(public, loaderAssets)

	socialLinks, err := jsonColumnToMap(settings.SocialLinks, true)
	if err != nil {
		logInvalidStoredSetting(ctx, "social_links", err)
	}
	if len(socialLinks) > 0 {
		value, err := structpb.NewStruct(socialLinks)
		if err != nil {
			logInvalidStoredSetting(ctx, "social_links", err)
		} else {
			public.SocialLinks = value
		}
	}
	return public, nil
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
func (s *Service) GetSetting(ctx context.Context, query GetSettingQuery) (*managev1.SiteSetting, error) {
	if err := s.requireView(ctx); err != nil {
		return nil, err
	}
	settings, err := loadSettingsRow(s.db.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	value, ok, decodeErr := decodedSettingValue(settings, query.Key)
	if !ok {
		return nil, domainerr.InvalidField("key", fmt.Sprintf("invalid setting key: %s", query.Key))
	}
	if decodeErr != nil {
		logInvalidStoredSetting(ctx, query.Key, decodeErr)
	}
	protoValue, err := structpb.NewValue(value)
	if err != nil {
		return nil, err
	}
	return &managev1.SiteSetting{Key: query.Key, Value: protoValue}, nil
}

// logInvalidStoredSetting records a stored setting that a read replaced by its
// empty fallback. The read still succeeds, so no request record holds it.
func logInvalidStoredSetting(ctx context.Context, key string, err error) {
	slog.ErrorContext(ctx, "Stored site setting is invalid; serving it empty", "domain", "sitesettings", "operation", "decode_stored_setting", "outcome", "failed", "key", key, failurecause.Attr(err))
}
