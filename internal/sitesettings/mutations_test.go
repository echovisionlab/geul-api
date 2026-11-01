package sitesettings_test

import (
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestSetSettingStoresTheValueUnderTheLockedSettingsAndAuditsIt(t *testing.T) {
	f := newFixture(t, nil, nil)

	response, err := f.set("company_name", structpb.NewStringValue("Acme"))

	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.Nil(t, response.Msg.OgGenerationRunId)
	require.Equal(t, "Acme", f.store.Stored().CompanyName)
	require.Equal(t, "Acme", f.get(t, "company_name").GetStringValue())
	require.Equal(t, [][]string{{"company_name"}}, f.changedFields(t))
	require.Equal(t, []authztest.Check{checkOf(f.admin.identityID)(policyv1.SiteSetting.Edit("1"))}, f.checker.Checks()[:1])
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, f.units.Steps()[:5], "the settings are locked before authority is checked")
}

func TestSetManySettingsAuditsEachChangedKeyOnceAndSkipsANoOp(t *testing.T) {
	f := newFixture(t, nil, nil)
	request := []*managev1.SiteSetting{
		setting("site_title", structpb.NewStringValue("Audit title")),
		setting("primary_color", structpb.NewStringValue("#123456")),
		setting("site_title", structpb.NewStringValue("Audit title")),
	}

	_, err := f.setMany(request...)
	require.NoError(t, err)
	_, err = f.setMany(request...)
	require.NoError(t, err)

	require.Equal(t, [][]string{{"primary_color", "site_title"}}, f.changedFields(t), "the repeated change audits nothing")
	stored := f.store.Stored()
	require.Equal(t, "Audit title", stored.SiteTitle)
	require.Equal(t, "#123456", stored.PrimaryColor)
}

func TestNestedOGSectionChangesAuditTheAggregateField(t *testing.T) {
	f := newFixture(t, nil, nil)
	home, err := structpb.NewValue(map[string]any{"background_color": "#123456"})
	require.NoError(t, err)

	_, err = f.set("og_image_config.home", home)

	require.NoError(t, err)
	require.Equal(t, [][]string{{"og_image_config"}}, f.changedFields(t))
	var config map[string]map[string]any
	require.NoError(t, json.Unmarshal(f.store.Stored().OGImageConfig, &config))
	require.Equal(t, map[string]map[string]any{"home": {"background_color": "#123456"}, "content": {}}, config)
}

func TestSettingsMutationsRequireFreshAdministratorAuthorityEvenWithoutAChange(t *testing.T) {
	f := newFixture(t, authztest.AllowAll().Deny(policyv1.SiteSetting.Edit("1")), nil)
	before := f.store.Stored()

	_, setErr := f.set("company_name", structpb.NewStringValue(before.CompanyName))
	_, manyErr := f.setMany(setting("company_name", structpb.NewStringValue(before.CompanyName)))
	loader := f.store.SeedFile("loader")
	_, addErr := f.settings.AddSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: loader}))
	_, removeErr := f.settings.RemoveSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.RemoveSiteLoaderAssetRequest{FileId: loader}))

	for _, err := range []error{setErr, manyErr, addErr, removeErr} {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		require.ErrorContains(t, err, "admin access required")
	}
	require.Equal(t, before, f.store.Stored())
	require.Empty(t, f.store.Loaders())
	require.Empty(t, f.audit.Records())
}

func TestSettingsMutationsRejectADeactivatedAdministrator(t *testing.T) {
	f := newFixture(t, nil, nil)
	f.fence.Deactivate(f.admin.identityID)

	_, err := f.set("company_name", structpb.NewStringValue("Deactivated"))

	require.Error(t, err)
	require.NotEqual(t, "Deactivated", f.store.Stored().CompanyName)
	require.Equal(t, mutation.StepRollback, lastStep(f))
}

func TestSettingsMutationsRequireAnAuthenticatedPrincipal(t *testing.T) {
	f := newFixture(t, nil, nil)

	_, err := f.settings.SetSetting(t.Context(), connect.NewRequest(&managev1.SetSettingRequest{Key: "company_name", Value: structpb.NewStringValue("Anonymous")}))

	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Empty(t, f.units.Steps())
}

func TestSettingValuesAreValidatedBeforeAuthority(t *testing.T) {
	f := newFixture(t, authztest.AllowAll().Deny(policyv1.SiteSetting.Edit("1")), nil)

	for name, call := range map[string]func() error{
		"an unknown key": func() error {
			_, err := f.set("support_email", structpb.NewStringValue("support@example.test"))
			return err
		},
		"an unknown key among several": func() error {
			_, err := f.setMany(setting("site_title", structpb.NewStringValue("Valid")), setting("privacy_email", structpb.NewStringValue("privacy@example.test")))
			return err
		},
		"a malformed value": func() error {
			_, err := f.set("primary_color", structpb.NewStringValue("red"))
			return err
		},
		"a value of the wrong type": func() error {
			_, err := f.setMany(setting("default_comments_enabled", structpb.NewStringValue("yes")))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(call()))
		})
	}
	require.Empty(t, f.checker.Checks(), "no authority was asked for")
	require.Empty(t, f.audit.Records())
}

func TestASettingsChangeRollsBackWhenALaterStepFails(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	ogDown := errors.New("OG planner unavailable")
	for name, test := range map[string]struct {
		audit   *audittest.Recorder
		prepare func(f *fixture)
		cause   error
		code    connect.Code
	}{
		"an audit failure":        {audit: &audittest.Recorder{Err: auditDown}, cause: auditDown},
		"an OG plan failure":      {prepare: func(f *fixture) { f.store.FailOG(ogDown) }, cause: ogDown},
		"an unpublished homepage": {code: connect.CodeInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil, test.audit)
			if test.prepare != nil {
				test.prepare(f)
			}
			before := f.store.Stored()
			logo := f.store.SeedFile("logo")
			settings := []*managev1.SiteSetting{
				setting("site_title", structpb.NewStringValue("Must roll back")),
				setting("logo_full_light_file_id", structpb.NewStringValue(logo)),
			}
			if test.code != 0 {
				settings = append(settings, setting("homepage_page_id", structpb.NewStringValue(f.store.SeedPage(false))))
			}

			_, err := f.setMany(settings...)

			if test.cause != nil {
				require.ErrorIs(t, err, test.cause)
			} else {
				require.Equal(t, test.code, connect.CodeOf(err))
			}
			require.Equal(t, before, f.store.Stored())
			require.Empty(t, f.store.Bindings())
			require.Equal(t, mutation.StepRollback, lastStep(f))
		})
	}
}

func TestOGSectionWritesKeepTheOtherSection(t *testing.T) {
	f := newFixture(t, nil, nil)
	stored := f.store.Stored()
	stored.OGImageConfig = []byte(`{"home":{"revision":"h0"},"content":{"revision":"c0"}}`)
	f.store.SetStored(stored)
	home, err := structpb.NewValue(map[string]any{"revision": "h1"})
	require.NoError(t, err)
	content, err := structpb.NewValue(map[string]any{"revision": "c1"})
	require.NoError(t, err)

	_, err = f.set("og_image_config.home", home)
	require.NoError(t, err)
	_, err = f.set("og_image_config.content", content)
	require.NoError(t, err)

	require.JSONEq(t, `{"home":{"revision":"h1"},"content":{"revision":"c1"}}`, string(f.store.Stored().OGImageConfig))
	require.Equal(t, [][]string{{"og_image_config"}, {"og_image_config"}}, f.changedFields(t))
}

func TestFileSettingsBindTheirReadyAssetAndReleaseItWhenCleared(t *testing.T) {
	f := newFixture(t, nil, nil)
	light, dark := f.store.SeedFile("logo"), f.store.SeedFile("logo")
	favicon := f.store.SeedFile("favicon")

	_, err := f.setMany(
		setting("logo_full_light_file_id", structpb.NewStringValue(light)),
		setting("logo_full_dark_file_id", structpb.NewStringValue(dark)),
		setting("favicon_file_id", structpb.NewStringValue(favicon)),
	)
	require.NoError(t, err)

	require.Equal(t, map[string]sitesettingstest.Binding{
		"logo_full_light": {SourceFileID: light, Kind: "logo"},
		"logo_full_dark":  {SourceFileID: dark, Kind: "logo"},
		"favicon":         {SourceFileID: favicon, Kind: "favicon"},
	}, f.store.Bindings())
	require.ElementsMatch(t, []string{light, dark, favicon}, f.store.Locked(), "every attached File is locked")
	require.Equal(t, light, f.get(t, "logo_full_light_file_id").GetStringValue())

	_, err = f.setMany(
		setting("logo_full_dark_file_id", structpb.NewNullValue()),
		setting("favicon_file_id", structpb.NewStringValue("")),
	)
	require.NoError(t, err)
	require.Equal(t, map[string]sitesettingstest.Binding{"logo_full_light": {SourceFileID: light, Kind: "logo"}}, f.store.Bindings())
	require.Equal(t, structpb.NewNullValue(), f.get(t, "logo_full_dark_file_id"))
}

func TestARejectedFileFailsTheWholeChange(t *testing.T) {
	f := newFixture(t, nil, nil)
	light, dark := f.store.SeedFile("logo"), f.store.SeedFile("logo")
	_, err := f.setMany(
		setting("logo_full_light_file_id", structpb.NewStringValue(light)),
		setting("logo_full_dark_file_id", structpb.NewStringValue(dark)),
	)
	require.NoError(t, err)
	before, bindings := f.store.Stored(), f.store.Bindings()
	webp := f.store.SeedFile("logo")
	f.store.RejectFile(webp, domainerr.InvalidField("file_id", "logo_full_dark_file_id does not accept image/webp"))

	_, err = f.setMany(
		setting("logo_full_light_file_id", structpb.NewStringValue(dark)),
		setting("logo_full_dark_file_id", structpb.NewStringValue(webp)),
	)

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, before, f.store.Stored())
	require.Equal(t, bindings, f.store.Bindings(), "the committed bindings stay")
}

func TestReferenceSettingsRequireAPublishedPageAndAnExistingMenu(t *testing.T) {
	f := newFixture(t, nil, nil)
	draft, published, menu := f.store.SeedPage(false), f.store.SeedPage(true), f.store.SeedMenu()

	_, draftErr := f.set("homepage_page_id", structpb.NewStringValue(draft))
	_, missingMenuErr := f.set("menu_footer_id", structpb.NewStringValue(published))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(draftErr))
	require.ErrorContains(t, draftErr, "must reference a published page")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(missingMenuErr))
	require.Nil(t, f.store.Stored().HomepagePageID)

	_, err := f.setMany(
		setting("homepage_page_id", structpb.NewStringValue(published)),
		setting("menu_header_id", structpb.NewStringValue(menu)),
	)
	require.NoError(t, err)
	stored := f.store.Stored()
	require.Equal(t, published, *stored.HomepagePageID)
	require.Equal(t, menu, *stored.MenuHeaderID)
}

func TestSettingsChangesRequestOneOGRunForWhatTheyAffect(t *testing.T) {
	f := newFixture(t, nil, nil)
	background := f.store.SeedFile("image")

	noImpact, err := f.set("company_name", structpb.NewStringValue("No OG impact"))
	require.NoError(t, err)
	overlapping, err := f.setMany(
		setting("site_title", structpb.NewStringValue("Updated Site")),
		setting("site_og_background_file_id", structpb.NewStringValue(background)),
		setting("primary_color", structpb.NewStringValue("#123456")),
	)
	require.NoError(t, err)
	unchanged, err := f.set("site_title", structpb.NewStringValue("Updated Site"))
	require.NoError(t, err)

	require.Nil(t, noImpact.Msg.OgGenerationRunId)
	require.Equal(t, "og-run-1", overlapping.Msg.GetOgGenerationRunId())
	require.Nil(t, unchanged.Msg.OgGenerationRunId)
	require.Equal(t, [][]string{
		{"company_name"},
		{"primary_color", "site_og_background_file_id", "site_title"},
	}, f.store.OGRequests(), "an unchanged value plans nothing")
}

func TestLoaderAssetsAddAndRemoveOnceWithTheirBinding(t *testing.T) {
	f := newFixture(t, nil, nil)
	loader := f.store.SeedFile("loader")
	add := func() error {
		_, err := f.settings.AddSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: " " + loader + " "}))
		return err
	}
	remove := func() error {
		_, err := f.settings.RemoveSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.RemoveSiteLoaderAssetRequest{FileId: loader}))
		return err
	}

	require.NoError(t, add())
	require.NoError(t, add())
	require.Equal(t, []string{loader}, f.store.Loaders())
	require.Equal(t, map[string]sitesettingstest.Binding{"loader_" + loader: {SourceFileID: loader, Kind: "loader"}}, f.store.Bindings())
	require.Equal(t, []string{loader, loader}, f.store.Locked())
	loaders := f.all(t).GetPublic().GetLoaderAssets()
	require.Len(t, loaders, 1)
	require.Equal(t, loader, loaders[0].GetFileId())
	require.Equal(t, sitesettingstest.Asset(loader, "loader").URL, loaders[0].GetAsset().GetUrl())

	require.NoError(t, remove())
	require.NoError(t, remove())
	require.Empty(t, f.store.Loaders())
	require.Empty(t, f.store.Bindings())
	require.Empty(t, f.all(t).GetPublic().GetLoaderAssets())
	require.Equal(t, [][]string{{"loader_file_ids"}, {"loader_file_ids"}}, f.changedFields(t), "only the changes are audited")
}

func TestLoaderAssetsRejectAFileBeforeAuthorityAndRollBackAFailedAudit(t *testing.T) {
	auditDown := errors.New("audit unavailable")
	f := newFixture(t, authztest.AllowAll().Deny(policyv1.SiteSetting.Edit("1")), nil)
	oversized := f.store.SeedFile("loader")
	f.store.RejectFile(oversized, domainerr.InvalidField("file_id", "loader file size is outside the supported range"))

	_, rejected := f.settings.AddSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: oversized}))
	_, blank := f.settings.AddSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: " "}))

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(rejected))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(blank))
	require.Empty(t, f.checker.Checks())

	f = newFixture(t, nil, &audittest.Recorder{Err: auditDown})
	loader := f.store.SeedFile("loader")
	_, err := f.settings.AddSiteLoaderAsset(f.admin.ctx, connect.NewRequest(&managev1.AddSiteLoaderAssetRequest{FileId: loader}))
	require.ErrorIs(t, err, auditDown)
	require.Empty(t, f.store.Loaders())
	require.Empty(t, f.store.Bindings())
	require.Equal(t, connect.CodeInternal, errs.Wrap(err).Code())
}
