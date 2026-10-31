package sitesettings

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetSettings returns all settings including sensitive ones (admin only)
func (s *SiteSettingService) GetSettings(
	ctx context.Context,
	req *connect.Request[managev1.GetSettingsRequest],
) (*connect.Response[managev1.GetSettingsResponse], error) {
	can, err := policyv1.SiteSetting.View(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	settings, err := s.loadSettingsRow(s.db.WithContext(ctx))
	if err != nil {
		return nil, errs.Internal(err)
	}

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
	}

	if v := optionalString(settings.HomepagePageID); v != nil {
		value := v.(string)
		public.HomepagePageId = &value
	}
	if v := optionalString(settings.MenuHeaderID); v != nil {
		value := v.(string)
		public.MenuHeaderId = &value
	}
	if v := optionalString(settings.MenuSecondaryID); v != nil {
		value := v.(string)
		public.MenuSecondaryId = &value
	}
	if v := optionalString(settings.MenuFooterID); v != nil {
		value := v.(string)
		public.MenuFooterId = &value
	}
	if v := optionalString(settings.MenuAvatarDropdownID); v != nil {
		value := v.(string)
		public.MenuAvatarDropdownId = &value
	}
	if v := optionalString(settings.GoogleAnalyticsID); v != nil {
		value := v.(string)
		public.GoogleAnalyticsId = &value
	}

	public.LogoFullLightAsset = s.optionalFileAsset(ctx, settings.LogoFullLightFileID, "logo")
	public.LogoFullDarkAsset = s.optionalFileAsset(ctx, settings.LogoFullDarkFileID, "logo")
	public.LogoSymbolLightAsset = s.optionalFileAsset(ctx, settings.LogoSymbolLightFileID, "logo")
	public.LogoSymbolDarkAsset = s.optionalFileAsset(ctx, settings.LogoSymbolDarkFileID, "logo")
	public.LogoEmailLightAsset = s.optionalFileAsset(ctx, settings.LogoEmailLightFileID, "email_image")
	public.LogoEmailDarkAsset = s.optionalFileAsset(ctx, settings.LogoEmailDarkFileID, "email_image")
	if settings.FaviconFileID != nil {
		public.FaviconAsset, public.FaviconAssetSet = s.assets.ProjectFavicon(ctx, s.db, *settings.FaviconFileID)
	}
	if settings.SiteOgBackgroundFileID != nil {
		public.SiteOgBackgroundAsset = s.getFileAsset(ctx, *settings.SiteOgBackgroundFileID, "image")
	}
	if settings.PrivacyOgBackgroundFileID != nil {
		public.PrivacyOgBackgroundAsset = s.getFileAsset(ctx, *settings.PrivacyOgBackgroundFileID, "image")
	}
	if settings.TermsOgBackgroundFileID != nil {
		public.TermsOgBackgroundAsset = s.getFileAsset(ctx, *settings.TermsOgBackgroundFileID, "image")
	}
	loaderAssets, err := s.loadSiteLoaderAssets(ctx, s.db)
	if err != nil {
		return nil, errs.Internal(err)
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

	all := &managev1.AllSettings{
		Public:  public,
		Runtime: &managev1.RuntimeSettings{SiteOrigin: s.siteOrigin},
	}
	all.OgImageConfig, err = jsonColumnToStruct(settings.OGImageConfig)
	if err != nil {
		logInvalidStoredSetting(ctx, "og_image_config", err)
	}

	return connect.NewResponse(&managev1.GetSettingsResponse{Settings: all}), nil
}

// GetSetting retrieves a single setting by key.
func (s *SiteSettingService) GetSetting(
	ctx context.Context,
	req *connect.Request[managev1.GetSettingRequest],
) (*connect.Response[managev1.GetSettingResponse], error) {
	can, err := policyv1.SiteSetting.View(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	settings, err := s.loadSettingsRow(s.db.WithContext(ctx))
	if err != nil {
		return nil, errs.Internal(err)
	}
	value, ok, decodeErr := decodedSettingValue(settings, req.Msg.Key)
	if !ok {
		return nil, errs.InvalidArgument("key", fmt.Sprintf("invalid setting key: %s", req.Msg.Key))
	}
	if decodeErr != nil {
		logInvalidStoredSetting(ctx, req.Msg.Key, decodeErr)
	}
	protoValue, err := structpb.NewValue(value)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.GetSettingResponse{Setting: &managev1.SiteSetting{
		Key: req.Msg.Key, Value: protoValue,
	}}), nil
}

// logInvalidStoredSetting records a stored setting that a read replaced by its
// empty fallback. The read still succeeds, so no request record holds it.
func logInvalidStoredSetting(ctx context.Context, key string, err error) {
	slog.ErrorContext(ctx, "Stored site setting is invalid; serving it empty", "domain", "sitesettings", "operation", "decode_stored_setting", "outcome", "failed", "key", key, failurecause.Attr(err))
}
