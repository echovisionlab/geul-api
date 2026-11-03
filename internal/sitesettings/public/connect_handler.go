package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ConnectHandler serves the public manifest RPC.
type ConnectHandler struct {
	manifest *Service
}

// NewConnectHandler returns the Connect adapter of manifest.
func NewConnectHandler(manifest *Service) *ConnectHandler {
	return &ConnectHandler{manifest: manifest}
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetRequest]) (*connect.Response[openv1.GetResponse], error) {
	manifest, err := h.manifest.GetManifest(ctx, GetManifestQuery{AcceptLanguage: req.Header().Get("Accept-Language")})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.GetResponse{Settings: settingsMessage(manifest.Settings), Menus: &openv1.Menus{
		Header:         menuItemMessages(manifest.Menus.Header),
		Secondary:      menuItemMessages(manifest.Menus.Secondary),
		Footer:         menuItemMessages(manifest.Menus.Footer),
		AvatarDropdown: menuItemMessages(manifest.Menus.AvatarDropdown),
	}}), nil
}

var linkTypeMap = map[string]openv1.LinkType{
	"custom":   openv1.LinkType_LINK_TYPE_CUSTOM,
	"page":     openv1.LinkType_LINK_TYPE_PAGE,
	"category": openv1.LinkType_LINK_TYPE_CATEGORY,
	"tag":      openv1.LinkType_LINK_TYPE_TAG,
	"series":   openv1.LinkType_LINK_TYPE_SERIES,
}

func settingsMessage(settings PublicSettings) *openv1.SiteSettings {
	loaderAssets := make([]*commonv1.AssetRef, 0, len(settings.LoaderAssets))
	for i := range settings.LoaderAssets {
		loaderAssets = append(loaderAssets, sitesettings.AssetMessage(&settings.LoaderAssets[i]))
	}
	return &openv1.SiteSettings{
		SiteTitle: settings.SiteTitle, MetaDescription: settings.MetaDescription,
		PrimaryColor: settings.PrimaryColor, CompanyName: settings.CompanyName,
		CompanyAddress: settings.CompanyAddress, TaxId: settings.TaxID,
		LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail,
		SiteOrigin:             settings.SiteOrigin,
		DefaultCommentsEnabled: settings.DefaultCommentsEnabled,
		SocialLinks:            settings.SocialLinks,
		GoogleAnalyticsId:      settings.GoogleAnalyticsID,
		LogoFullLightAsset:     sitesettings.AssetMessage(settings.LogoFullLight),
		LogoFullDarkAsset:      sitesettings.AssetMessage(settings.LogoFullDark),
		LogoSymbolLightAsset:   sitesettings.AssetMessage(settings.LogoSymbolLight),
		LogoSymbolDarkAsset:    sitesettings.AssetMessage(settings.LogoSymbolDark),
		FaviconAsset:           sitesettings.AssetMessage(settings.Favicon),
		FaviconAssetSet:        sitesettings.FaviconSetMessage(settings.FaviconSet),
		SiteOgAsset:            sitesettings.AssetMessage(settings.SiteOg),
		LoaderAssets:           loaderAssets,
	}
}

func menuItemMessages(links []MenuLink) []*openv1.MenuItem {
	if links == nil {
		return nil
	}
	messages := make([]*openv1.MenuItem, 0, len(links))
	for _, link := range links {
		messages = append(messages, &openv1.MenuItem{
			Id: link.ID, Label: link.Label, LinkType: linkTypeMap[link.LinkType],
			Url: link.URL, TargetId: link.TargetID, TargetSlug: link.TargetSlug,
			OpenInNewTab: link.OpenInNewTab, Children: menuItemMessages(link.Children),
		})
	}
	return messages
}
