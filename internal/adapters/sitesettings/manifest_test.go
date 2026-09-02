package sitesettings

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestManifestMenuTargetSlugUsesStableFallbackWithoutDatabase(t *testing.T) {
	var adapter ManifestMenus
	fallback := "stable"
	require.Nil(t, adapter.TargetSlug(t.Context(), nil, nil))
	require.Equal(t, &fallback, adapter.TargetSlug(t.Context(), nil, &model.MenuItem{TargetSlug: &fallback}))
	require.Equal(t, &fallback, adapter.TargetSlug(t.Context(), nil, &model.MenuItem{
		LinkType: "page", TargetSlug: &fallback,
	}))
	emptyTargetID := " "
	require.Equal(t, &fallback, adapter.TargetSlug(t.Context(), nil, &model.MenuItem{
		LinkType: "page", TargetID: &emptyTargetID, TargetSlug: &fallback,
	}))
	targetID := "target-id"
	require.Equal(t, &fallback, adapter.TargetSlug(t.Context(), nil, &model.MenuItem{
		LinkType: "custom", TargetID: &targetID, TargetSlug: &fallback,
	}))
}

func TestManifestMenuProjectionUsesTranslatedAndFixedLocaleLabels(t *testing.T) {
	fixedMode := model.MenuItemLocalizationModeFixedLocale
	english := "en"
	japanese := "ja"
	sourceItems := []model.MenuItem{
		{ID: "home", Label: "홈", LinkType: "page"},
		{ID: "brand", Label: "브랜드", LinkType: "page", LocalizationMode: &fixedMode, FixedLocale: &english},
		{ID: "about", Label: "소개", LinkType: "page", Children: []model.MenuItem{{
			ID: "team", Label: "팀", LinkType: "page", LocalizationMode: &fixedMode, FixedLocale: &japanese,
		}}},
	}
	rows := map[string]manifestMenuTranslationRow{
		"ko": {Locale: "ko", ItemsJSON: []byte(`[
			{"id":"home","label":"홈"},{"id":"about","label":"소개"}
		]`)},
		"fr": {Locale: "fr", ItemsJSON: []byte(`[
			{"id":"home","label":"Accueil"},{"id":"brand","label":"Marque"},
			{"id":"about","label":"À propos","children":[{"id":"team","label":"Équipe"}]}
		]`)},
		"en": {Locale: "en", ItemsJSON: []byte(`[
			{"id":"home","label":"Home"},{"id":"brand","label":"Brand"},
			{"id":"about","label":"About","children":[{"id":"team","label":"Team"}]}
		]`)},
		"ja": {Locale: "ja", ItemsJSON: []byte(`[
			{"id":"home","label":"ホーム"},{"id":"brand","label":"ブランド"},
			{"id":"about","label":"概要","children":[{"id":"team","label":"チーム"}]}
		]`)},
	}
	labels, err := manifestMenuLabels(rows)
	require.NoError(t, err)
	storedLocales := map[string]struct{}{
		"ko": {},
		"fr": {},
		"en": {},
		"ja": {},
	}
	projected := projectManifestMenuLabels(sourceItems, labels, storedLocales, "fr", "ko")
	require.Equal(t, "Accueil", projected[0].Label)
	require.Equal(t, "Brand", projected[1].Label)
	require.Equal(t, "À propos", projected[2].Label)
	require.Equal(t, "チーム", projected[2].Children[0].Label)
}

func TestManifestMenuProjectionFallsBackToSourceForUnavailableFixedLocale(t *testing.T) {
	fixedMode := model.MenuItemLocalizationModeFixedLocale
	english := "en"
	items := []model.MenuItem{{
		ID: "brand", Label: "브랜드", LocalizationMode: &fixedMode, FixedLocale: &english,
	}}
	projected := projectManifestMenuLabels(
		items,
		map[string]map[string]string{"ko": {"brand": "브랜드"}},
		map[string]struct{}{"ko": {}},
		"fr", "ko",
	)
	require.Equal(t, "브랜드", projected[0].Label)
}

func TestManifestMenuProjectionDistinguishesExplicitEmptyFromMissing(t *testing.T) {
	sourceItems := []model.MenuItem{
		{ID: "home", Label: "Home"},
		{ID: "about", Label: "About"},
	}
	labels, err := manifestMenuLabels(map[string]manifestMenuTranslationRow{
		"ko": {Locale: "ko", ItemsJSON: []byte(`[{"id":"home","label":"Home"},{"id":"about","label":"About"}]`)},
		"fr": {Locale: "fr", ItemsJSON: []byte(`[{"id":"home","label":""}]`)},
	})
	require.NoError(t, err)
	require.Contains(t, labels["fr"], "home")
	require.Empty(t, labels["fr"]["home"])
	require.NotContains(t, labels["fr"], "about")

	projected := projectManifestMenuLabels(
		sourceItems, labels, map[string]struct{}{"ko": {}, "fr": {}}, "fr", "ko",
	)
	require.Empty(t, projected[0].Label, "present empty is an explicit rendered value")
	require.Equal(t, "About", projected[1].Label, "missing target value falls back to source")
}

func TestManifestMenuProjectionNeverFallsBackToOldRootForMissingSourceValue(t *testing.T) {
	items := []model.MenuItem{{ID: "home", Label: "오래된 루트 값"}}
	projected := projectManifestMenuLabels(
		items,
		map[string]map[string]string{"ko": {}, "fr": {}},
		map[string]struct{}{"ko": {}, "fr": {}},
		"fr", "ko",
	)
	require.Empty(t, projected[0].Label)
}
