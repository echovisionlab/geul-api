package public_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/sitesettings/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// projection serves fixed settings, menus and the assets of the Files and
// assets it names.
type projection struct {
	settings     sitesettings.Settings
	menus        public.MenuSlots
	sourceAssets map[string]sitesettings.Asset // by File ID
	assets       map[string]sitesettings.Asset // by asset ID
	loaders      []sitesettings.Asset
	slugs        map[string]string // by target ID
	language     string
}

func (p *projection) Settings(context.Context) (sitesettings.Settings, error) { return p.settings, nil }

func (p *projection) Menus(_ context.Context, _ sitesettings.Settings, acceptLanguage string) (public.MenuSlots, error) {
	p.language = acceptLanguage
	return p.menus, nil
}

func (p *projection) ReadySourceAsset(_ context.Context, fileID, _ string) *sitesettings.Asset {
	if asset, ok := p.sourceAssets[fileID]; ok {
		return &asset
	}
	return nil
}

func (p *projection) ReadyAsset(_ context.Context, assetID string) *sitesettings.Asset {
	if asset, ok := p.assets[assetID]; ok {
		return &asset
	}
	return nil
}

func (p *projection) Favicon(context.Context, string) (*sitesettings.Asset, *sitesettings.FaviconSet) {
	return nil, nil
}

func (p *projection) LoaderAssets(context.Context) ([]sitesettings.Asset, error) {
	return p.loaders, nil
}

func (p *projection) TargetSlug(_ context.Context, item public.MenuItem) *string {
	if item.TargetID != nil {
		if slug, ok := p.slugs[*item.TargetID]; ok {
			return &slug
		}
	}
	return item.TargetSlug
}

func asset(id string) sitesettings.Asset {
	return sitesettings.Asset{ID: id, URL: "https://cdn.example.com/asset/" + id + "/" + id + ".png", Extension: "png", MIMEType: "image/png"}
}

func manifest(t *testing.T, p *projection, checker *authztest.Checker, ctx context.Context) *openv1.GetResponse {
	t.Helper()
	service, err := public.NewService(public.Deps{Projection: p, SpiceDB: checker, SiteOrigin: " https://www.example.test/ "})
	require.NoError(t, err)
	request := connect.NewRequest(&openv1.GetRequest{})
	request.Header().Set("Accept-Language", "ko-KR")
	response, err := public.NewConnectHandler(service).Get(ctx, request)
	require.NoError(t, err)
	return response.Msg
}

func TestManifestProjectsTheSettingsWithTheRuntimeOriginAndFallbackLogos(t *testing.T) {
	logo, og := "logo-file", "og-asset"
	analytics, empty := "G-ABC123", ""
	p := &projection{
		settings: sitesettings.Settings{
			SiteTitle: "Site", CompanyName: "Company", LegalEmail: "legal@example.test", ContactEmail: "contact@example.test",
			MetaDescription: "{{site.title}} at {{site.url}}, {{unknown.value}}", PrimaryColor: "#123456",
			GoogleAnalyticsID: &analytics, LogoFullLightFileID: &logo, LogoSymbolDarkFileID: &empty, SiteOgAssetID: &og,
			SocialLinks: []byte(`{"website":"https://example.test","rank":1}`),
		},
		sourceAssets: map[string]sitesettings.Asset{logo: asset("logo-asset")},
		assets:       map[string]sitesettings.Asset{og: asset(og)},
		loaders:      []sitesettings.Asset{asset("loader-asset")},
	}

	settings := manifest(t, p, &authztest.Checker{}, t.Context()).GetSettings()

	require.Equal(t, "https://www.example.test", settings.GetSiteOrigin())
	require.Equal(t, "Site at https://www.example.test, {{unknown.value}}", settings.GetMetaDescription())
	require.Equal(t, "legal@example.test", settings.GetLegalEmail())
	require.Equal(t, "contact@example.test", settings.GetContactEmail())
	require.Equal(t, analytics, settings.GetGoogleAnalyticsId())
	for _, logo := range []*commonv1.AssetRef{
		settings.GetLogoFullLightAsset(), settings.GetLogoFullDarkAsset(), settings.GetLogoSymbolLightAsset(), settings.GetLogoSymbolDarkAsset(),
	} {
		require.Equal(t, "https://cdn.example.com/asset/logo-asset/logo-asset.png", logo.GetUrl(), "every display slot falls back to the configured logo")
	}
	require.Equal(t, "https://cdn.example.com/asset/og-asset/og-asset.png", settings.GetSiteOgAsset().GetUrl())
	require.Len(t, settings.GetLoaderAssets(), 1)
	require.Equal(t, "loader-asset", settings.GetLoaderAssets()[0].GetAssetId())
	require.Equal(t, map[string]string{"website": "https://example.test"}, settings.GetSocialLinks(), "only text links are served")
	require.Nil(t, settings.GetFaviconAsset())
}

func TestManifestServesTheMenuItemsTheCallerMaySee(t *testing.T) {
	targetID, newTab := uuid.NewString(), true
	roles := func(roles ...string) *public.MenuVisibility {
		return &public.MenuVisibility{Mode: "roles", Roles: roles}
	}
	p := &projection{
		menus: public.MenuSlots{Header: []public.MenuItem{
			{ID: "page", Label: "Page", LinkType: "page", TargetID: &targetID, OpenInNewTab: &newTab, Children: []public.MenuItem{
				{ID: "guest", Label: "Guest", LinkType: "custom", Visibility: &public.MenuVisibility{Mode: "guest"}},
				{ID: "authors", Label: "Authors", LinkType: "custom", Visibility: roles(" Author ")},
			}},
			{ID: "members", Label: "Members", LinkType: "tag", Visibility: &public.MenuVisibility{Mode: "authenticated"}},
			{ID: "admins", Label: "Admins", LinkType: "series", Visibility: roles("admin")},
		}},
		slugs: map[string]string{targetID: "about"},
	}
	identityID := uuid.NewString()
	author := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})
	checker := (&authztest.Checker{}).Allow(policyv1.Platform.IsAuthor())

	guestMenus := manifest(t, p, checker, t.Context()).GetMenus()
	authorMenus := manifest(t, p, checker, author).GetMenus()

	require.Equal(t, "ko-KR", p.language)
	header := guestMenus.GetHeader()
	require.Len(t, header, 1)
	require.Equal(t, openv1.LinkType_LINK_TYPE_PAGE, header[0].GetLinkType())
	require.Equal(t, "about", header[0].GetTargetSlug())
	require.Equal(t, targetID, header[0].GetTargetId())
	require.True(t, header[0].GetOpenInNewTab())
	require.Equal(t, []string{"guest"}, itemIDs(header[0].GetChildren()))
	require.Equal(t, []string{"page", "members"}, itemIDs(authorMenus.GetHeader()))
	require.Equal(t, []string{"authors"}, itemIDs(authorMenus.GetHeader()[0].GetChildren()))
	require.Equal(t, openv1.LinkType_LINK_TYPE_TAG, authorMenus.GetHeader()[1].GetLinkType())
	require.Empty(t, authorMenus.GetFooter())
}

func itemIDs(items []*openv1.MenuItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetId())
	}
	return ids
}
