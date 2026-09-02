package sitesettings

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
		SupportEmail:           settings.SupportEmail,
		PrivacyEmail:           settings.PrivacyEmail,
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

	if settings.LogoLightFileID != nil {
		public.LogoLightAsset = s.getFileAsset(ctx, *settings.LogoLightFileID, "logo")
	}
	if settings.LogoDarkFileID != nil {
		public.LogoDarkAsset = s.getFileAsset(ctx, *settings.LogoDarkFileID, "logo")
	}
	if settings.LogoEmailFileID != nil {
		public.LogoEmailAsset = s.getFileAsset(ctx, *settings.LogoEmailFileID, "email_image")
	}
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
		slog.Error("failed to load site loader assets", "error", err)
		return nil, errs.Internal(err)
	}
	applyLoaderAssetsToPublicSettings(public, loaderAssets)

	if socialLinks := jsonColumnToMap(settings.SocialLinks, "social_links", true); len(socialLinks) > 0 {
		value, err := structpb.NewStruct(socialLinks)
		if err != nil {
			slog.Warn("failed to convert social_links to struct", "error", err)
		} else {
			public.SocialLinks = value
		}
	}

	all := &managev1.AllSettings{
		Public:  public,
		Runtime: &managev1.RuntimeSettings{SiteOrigin: s.siteOrigin},
	}
	all.OgImageConfig = jsonColumnToStruct(settings.OGImageConfig, "og_image_config")

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
	value, ok := settingValue(settings, req.Msg.Key)
	if !ok {
		return nil, errs.InvalidArgument("key", fmt.Sprintf("invalid setting key: %s", req.Msg.Key))
	}
	protoValue, err := structpb.NewValue(value)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.GetSettingResponse{Setting: &managev1.SiteSetting{
		Key: req.Msg.Key, Value: protoValue,
	}}), nil
}
