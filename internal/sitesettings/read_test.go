package sitesettings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestGetSettingsProjectsTheReadyAssetOfEachFileSetting(t *testing.T) {
	f := newFixture(t, nil, nil)
	light, emailLight := f.store.SeedFile("logo"), f.store.SeedFile("email_image")
	favicon, background := f.store.SeedFile("favicon"), f.store.SeedFile("image")
	unready := f.store.SeedFile()
	empty := ""
	stored := f.store.Stored()
	stored.LogoFullLightFileID, stored.LogoEmailLightFileID = &light, &emailLight
	stored.LogoFullDarkFileID, stored.LogoSymbolLightFileID = &unready, &empty
	stored.FaviconFileID, stored.TermsOgBackgroundFileID = &favicon, &background
	stored.HomepagePageID, stored.MenuFooterID = &empty, &light
	stored.SocialLinks = []byte(`{"website":"https://example.test"}`)
	stored.OGImageConfig = []byte(`{"home":{"title":"Home"}}`)
	f.store.SetStored(stored)

	all := f.all(t)

	public := all.GetPublic()
	require.Equal(t, sitesettingstest.Asset(light, "logo").URL, public.GetLogoFullLightAsset().GetUrl())
	require.Equal(t, sitesettingstest.Asset(emailLight, "email_image").URL, public.GetLogoEmailLightAsset().GetUrl())
	require.Nil(t, public.GetLogoFullDarkAsset(), "a File without a ready asset is served without it")
	require.Nil(t, public.GetLogoSymbolLightAsset())
	require.Equal(t, sitesettingstest.Asset(favicon, "favicon").URL, public.GetFaviconAsset().GetUrl())
	require.Equal(t, sitesettingstest.Asset(background, "image").URL, public.GetTermsOgBackgroundAsset().GetUrl())
	require.Nil(t, public.GetSiteOgBackgroundAsset())
	require.Nil(t, public.HomepagePageId, "an empty reference reads as unset")
	require.Equal(t, light, public.GetMenuFooterId())
	require.Equal(t, stored.DefaultMapThemeID, public.GetDefaultMapThemeId())
	require.Equal(t, map[string]any{"website": "https://example.test"}, public.GetSocialLinks().AsMap())
	require.Equal(t, map[string]any{"home": map[string]any{"title": "Home"}}, all.GetOgImageConfig().AsMap())
	require.Equal(t, "https://www.example.test", all.GetRuntime().GetSiteOrigin())
	require.NotNil(t, public.GetLoaderAssets())
}

func TestReadsServeAnUndecodableStoredValueAsItsFallback(t *testing.T) {
	f := newFixture(t, nil, nil)
	stored := f.store.Stored()
	stored.SocialLinks = []byte(`{"broken"`)
	stored.OGImageConfig = []byte(`["not an object"]`)
	f.store.SetStored(stored)

	all := f.all(t)

	require.Nil(t, all.GetPublic().GetSocialLinks())
	require.Nil(t, all.GetOgImageConfig())
	require.Equal(t, map[string]any{}, f.get(t, "social_links").GetStructValue().AsMap())
	require.Equal(t, structpb.NewNullValue(), f.get(t, "og_image_config.home"))
}

func TestReadsRequireTheSettingsViewAuthority(t *testing.T) {
	f := newFixture(t, authztest.AllowAll().Deny(policyv1.SiteSetting.View("1")), nil)

	_, allErr := f.settings.GetSettings(f.admin.ctx, connect.NewRequest(&managev1.GetSettingsRequest{}))
	_, oneErr := f.settings.GetSetting(f.admin.ctx, connect.NewRequest(&managev1.GetSettingRequest{Key: "site_title"}))

	for _, err := range []error{allErr, oneErr} {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "admin access required")
	}
	require.Equal(t, []authztest.Check{
		checkOf(f.admin.identityID)(policyv1.SiteSetting.View("1")),
		checkOf(f.admin.identityID)(policyv1.SiteSetting.View("1")),
	}, f.checker.Checks())
}

func TestGetSettingRejectsAnUnknownKey(t *testing.T) {
	f := newFixture(t, nil, nil)

	_, err := f.settings.GetSetting(f.admin.ctx, connect.NewRequest(&managev1.GetSettingRequest{Key: "support_email"}))

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "invalid setting key: support_email")
}
