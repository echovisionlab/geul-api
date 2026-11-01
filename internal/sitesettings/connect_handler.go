package sitesettings

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

// ConnectHandler serves the Site Settings management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedSiteSettingServiceHandler
	settings *Service
}

// NewConnectHandler returns the Connect adapter of settings.
func NewConnectHandler(settings *Service) *ConnectHandler {
	return &ConnectHandler{settings: settings}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// requestedValue is the plain value of a requested setting; an absent value
// is null.
func requestedValue(value *structpb.Value) structured.Value {
	if value == nil {
		return nil
	}
	return value.AsInterface()
}

// requestedSettings converts requested setting messages into command values.
func requestedSettings(settings []*managev1.SiteSetting) []Setting {
	requested := make([]Setting, 0, len(settings))
	for _, setting := range settings {
		requested = append(requested, Setting{Key: setting.Key, Value: requestedValue(setting.Value)})
	}
	return requested
}

func (h *ConnectHandler) GetSettings(ctx context.Context, _ *connect.Request[managev1.GetSettingsRequest]) (*connect.Response[managev1.GetSettingsResponse], error) {
	settings, err := h.settings.GetSettings(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.GetSettingsResponse{Settings: allSettingsMessage(ctx, settings)}), nil
}

func (h *ConnectHandler) GetSetting(ctx context.Context, req *connect.Request[managev1.GetSettingRequest]) (*connect.Response[managev1.GetSettingResponse], error) {
	setting, err := h.settings.GetSetting(ctx, GetSettingQuery{Key: req.Msg.Key})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	value, err := structpb.NewValue(setting.Value)
	return respond(&managev1.GetSettingResponse{Setting: &managev1.SiteSetting{Key: setting.Key, Value: value}}, err)
}

func (h *ConnectHandler) SetSetting(ctx context.Context, req *connect.Request[managev1.SetSettingRequest]) (*connect.Response[managev1.SetSettingResponse], error) {
	update, err := h.settings.SetSetting(ctx, SetSettingCommand{Key: req.Msg.Key, Value: requestedValue(req.Msg.Value)})
	return respond(&managev1.SetSettingResponse{Success: true, OgGenerationRunId: update.OGGenerationRunID}, err)
}

func (h *ConnectHandler) SetManySettings(ctx context.Context, req *connect.Request[managev1.SetManySettingsRequest]) (*connect.Response[managev1.SetManySettingsResponse], error) {
	update, err := h.settings.SetManySettings(ctx, SetManySettingsCommand{Settings: requestedSettings(req.Msg.Settings)})
	return respond(&managev1.SetManySettingsResponse{Success: true, OgGenerationRunId: update.OGGenerationRunID}, err)
}

func (h *ConnectHandler) AddSiteLoaderAsset(ctx context.Context, req *connect.Request[managev1.AddSiteLoaderAssetRequest]) (*connect.Response[managev1.AddSiteLoaderAssetResponse], error) {
	err := h.settings.AddSiteLoaderAsset(ctx, AddSiteLoaderAssetCommand{FileID: req.Msg.FileId})
	return respond(&managev1.AddSiteLoaderAssetResponse{Success: true}, err)
}

func (h *ConnectHandler) RemoveSiteLoaderAsset(ctx context.Context, req *connect.Request[managev1.RemoveSiteLoaderAssetRequest]) (*connect.Response[managev1.RemoveSiteLoaderAssetResponse], error) {
	err := h.settings.RemoveSiteLoaderAsset(ctx, RemoveSiteLoaderAssetCommand{FileID: req.Msg.FileId})
	return respond(&managev1.RemoveSiteLoaderAssetResponse{Success: true}, err)
}

func allSettingsMessage(ctx context.Context, settings AdminSettings) *managev1.AllSettings {
	public := &managev1.PublicSettings{
		SiteTitle:                settings.SiteTitle,
		CompanyName:              settings.CompanyName,
		CompanyAddress:           settings.CompanyAddress,
		TaxId:                    settings.TaxID,
		LegalEmail:               settings.LegalEmail,
		ContactEmail:             settings.ContactEmail,
		PrimaryColor:             settings.PrimaryColor,
		DefaultCommentsEnabled:   settings.DefaultCommentsEnabled,
		DefaultMapThemeId:        settings.DefaultMapThemeID,
		MetaDescription:          settings.MetaDescription,
		HomepagePageId:           settings.HomepagePageID,
		MenuHeaderId:             settings.MenuHeaderID,
		MenuSecondaryId:          settings.MenuSecondaryID,
		MenuFooterId:             settings.MenuFooterID,
		MenuAvatarDropdownId:     settings.MenuAvatarDropdownID,
		GoogleAnalyticsId:        settings.GoogleAnalyticsID,
		LogoFullLightAsset:       AssetMessage(settings.LogoFullLight),
		LogoFullDarkAsset:        AssetMessage(settings.LogoFullDark),
		LogoSymbolLightAsset:     AssetMessage(settings.LogoSymbolLight),
		LogoSymbolDarkAsset:      AssetMessage(settings.LogoSymbolDark),
		LogoEmailLightAsset:      AssetMessage(settings.LogoEmailLight),
		LogoEmailDarkAsset:       AssetMessage(settings.LogoEmailDark),
		FaviconAsset:             AssetMessage(settings.Favicon),
		FaviconAssetSet:          FaviconSetMessage(settings.FaviconSet),
		SiteOgBackgroundAsset:    AssetMessage(settings.SiteOgBackground),
		PrivacyOgBackgroundAsset: AssetMessage(settings.PrivacyOgBackground),
		TermsOgBackgroundAsset:   AssetMessage(settings.TermsOgBackground),
		LoaderAssets:             make([]*managev1.SiteLoaderAsset, 0, len(settings.LoaderAssets)),
	}
	for _, loader := range settings.LoaderAssets {
		public.LoaderAssets = append(public.LoaderAssets, &managev1.SiteLoaderAsset{FileId: loader.FileID, Asset: AssetMessage(&loader.Asset)})
	}
	public.SocialLinks = storedStruct(ctx, "social_links", settings.SocialLinks)
	return &managev1.AllSettings{
		Public:        public,
		Runtime:       &managev1.RuntimeSettings{SiteOrigin: settings.SiteOrigin},
		OgImageConfig: storedStruct(ctx, "og_image_config", settings.OGImageConfig),
	}
}

// storedStruct is the API form of a decoded JSON setting; nil stays nil, and
// a value the API cannot carry is logged and served empty.
func storedStruct(ctx context.Context, key string, fields structured.Fields) *structpb.Struct {
	if fields == nil {
		return nil
	}
	value, err := structpb.NewStruct(fields)
	if err != nil {
		logInvalidStoredSetting(ctx, key, err)
		return nil
	}
	return value
}

// AssetMessage is the API form of a ready asset; nil stays nil.
func AssetMessage(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId:          asset.ID,
		Url:              asset.URL,
		Extension:        asset.Extension,
		MimeType:         asset.MIMEType,
		FileSize:         asset.FileSize,
		Sha256:           asset.SHA256,
		Disposition:      commonv1.AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

// FaviconSetMessage is the API form of a generated favicon set; nil stays
// nil.
func FaviconSetMessage(set *FaviconSet) *commonv1.FaviconAssetSet {
	if set == nil {
		return nil
	}
	return &commonv1.FaviconAssetSet{
		IconIco:            AssetMessage(set.ICO),
		IconPng_16:         AssetMessage(set.PNG16),
		IconPng_32:         AssetMessage(set.PNG32),
		IconPng_48:         AssetMessage(set.PNG48),
		AppleTouchIcon_180: AssetMessage(set.AppleTouch180),
		ManifestIcon_192:   AssetMessage(set.Manifest192),
		ManifestIcon_512:   AssetMessage(set.Manifest512),
		IconSvg:            AssetMessage(set.SVG),
	}
}
