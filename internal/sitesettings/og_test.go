package sitesettings

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestClassifySiteSettingOgInvalidationMatrix(t *testing.T) {
	base := model.SiteSettings{
		SiteTitle:     "Before",
		PrimaryColor:  "#111111",
		OGImageConfig: []byte(`{"home":{"logo":1},"content":{"title":1}}`),
	}
	testCases := []struct {
		name   string
		mutate func(*model.SiteSettings)
		keys   []string
		want   OGInvalidation
	}{
		{name: "site title invalidates all", mutate: func(value *model.SiteSettings) { value.SiteTitle = "After" }, keys: []string{"site_title"}, want: OGInvalidation{All: true}},
		{name: "primary color invalidates all", mutate: func(value *model.SiteSettings) { value.PrimaryColor = "#222222" }, keys: []string{"primary_color"}, want: OGInvalidation{All: true}},
		{name: "light logo invalidates all", mutate: func(value *model.SiteSettings) { value.LogoLightFileID = new("light") }, keys: []string{"logo_light_file_id"}, want: OGInvalidation{All: true}},
		{name: "site background invalidates site", mutate: func(value *model.SiteSettings) { value.SiteOgBackgroundFileID = new("site") }, keys: []string{"site_og_background_file_id"}, want: OGInvalidation{Site: true}},
		{name: "privacy background invalidates privacy", mutate: func(value *model.SiteSettings) { value.PrivacyOgBackgroundFileID = new("privacy") }, keys: []string{"privacy_og_background_file_id"}, want: OGInvalidation{Privacy: true}},
		{name: "terms background invalidates terms", mutate: func(value *model.SiteSettings) { value.TermsOgBackgroundFileID = new("terms") }, keys: []string{"terms_og_background_file_id"}, want: OGInvalidation{Terms: true}},
		{name: "home config invalidates site", mutate: func(value *model.SiteSettings) {
			value.OGImageConfig = []byte(`{"content":{"title":1},"home":{"logo":2}}`)
		}, keys: []string{"og_image_config"}, want: OGInvalidation{Site: true}},
		{name: "content config invalidates non-site content", mutate: func(value *model.SiteSettings) {
			value.OGImageConfig = []byte(`{"home":{"logo":1},"content":{"title":2}}`)
		}, keys: []string{"og_image_config"}, want: OGInvalidation{Content: true}},
		{name: "explicit home config invalidates site only", mutate: func(value *model.SiteSettings) {
			value.OGImageConfig = []byte(`{"home":{"logo":2},"content":{"title":1}}`)
		}, keys: []string{"og_image_config.home"}, want: OGInvalidation{Site: true}},
		{name: "explicit content config invalidates content only", mutate: func(value *model.SiteSettings) {
			value.OGImageConfig = []byte(`{"home":{"logo":1},"content":{"title":2}}`)
		}, keys: []string{"og_image_config.content"}, want: OGInvalidation{Content: true}},
		{name: "structural equality ignores json order", mutate: func(value *model.SiteSettings) {
			value.OGImageConfig = []byte(`{"content":{"title":1},"home":{"logo":1}}`)
		}, keys: []string{"og_image_config"}},
		{name: "dark email and favicon do not invalidate", mutate: func(value *model.SiteSettings) {
			value.LogoDarkFileID = new("dark")
			value.LogoEmailFileID = new("email")
			value.FaviconFileID = new("favicon")
		}, keys: []string{"logo_dark_file_id", "logo_email_file_id", "favicon_file_id"}},
		{name: "overlapping set many remains one combined classification", mutate: func(value *model.SiteSettings) {
			value.SiteTitle = "After"
			value.SiteOgBackgroundFileID = new("site")
			value.PrivacyOgBackgroundFileID = new("privacy")
		}, keys: []string{"site_title", "site_og_background_file_id", "privacy_og_background_file_id", "site_title"}, want: OGInvalidation{All: true, Site: true, Privacy: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			after := base
			testCase.mutate(&after)
			assert.Equal(t, testCase.want, ClassifyOGInvalidation(&base, &after, testCase.keys))
		})
	}
}

func TestApplyOgImageConfigSectionInitializesAndPreservesRequiredSections(t *testing.T) {
	settings := &model.SiteSettings{}
	assert.NoError(t, applyOgImageConfigSection(settings, "home", structured.Fields{"revision": "h1"}))
	var root structured.Fields
	assert.NoError(t, json.Unmarshal(settings.OGImageConfig, &root))
	assert.Equal(t, structured.Fields{"revision": "h1"}, root["home"])
	assert.Equal(t, structured.Fields{}, root["content"])

	assert.NoError(t, applyOgImageConfigSection(settings, "content", structured.Fields{"revision": "c1"}))
	assert.NoError(t, json.Unmarshal(settings.OGImageConfig, &root))
	assert.Equal(t, structured.Fields{"revision": "h1"}, root["home"])
	assert.Equal(t, structured.Fields{"revision": "c1"}, root["content"])

	settings.OGImageConfig = []byte(`{"home":[],"content":{}}`)
	assert.ErrorContains(t, applyOgImageConfigSection(settings, "content", structured.Fields{}), "og_image_config.home")
}
