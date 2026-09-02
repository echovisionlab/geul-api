package menu

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMenuInterchangePreservesAbsentAndExplicitEmptyTargets(t *testing.T) {
	t.Parallel()
	plan := &translation.ExtractionPlan{
		EntityType: "menu", EntityID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
		Units: []translation.Unit{
			{UnitID: "item:home:label", ContainerType: translation.ContainerTypeBlock, ContainerID: "home", FieldName: "label"},
			{UnitID: "item:about:label", ContainerType: translation.ContainerTypeBlock, ContainerID: "about", FieldName: "label"},
		},
	}
	current := projectMenuInterchangeTargets(plan, map[string]string{"home": ""}, true)
	require.Contains(t, current, "item:home:label")
	require.Empty(t, current["item:home:label"].TranslatedText)
	require.NotContains(t, current, "item:about:label")

	desired := mergeMenuInterchangeTargets(
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		current,
		map[string]translation.UnitResult{
			"item:about:label": {UnitID: "item:about:label", TranslatedText: "About"},
		},
	)
	labels, err := menuInterchangeLabels(plan, desired)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"home": "", "about": "About"}, labels)
}

func TestMenuInterchangeRejectsTargetStructuralUnits(t *testing.T) {
	t.Parallel()
	mutation := TranslationInterchangeMutation{
		MenuID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Plan: &translation.ExtractionPlan{
			EntityType: "menu", EntityID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
			Units: []translation.Unit{{
				UnitID: "item:home:url", ContainerType: translation.ContainerTypeBlock,
				ContainerID: "home", FieldName: "url",
			}},
		},
		Targets: map[string]translation.UnitResult{
			"item:home:url": {UnitID: "item:home:url", TranslatedText: "/changed"},
		},
	}
	require.Error(t, validateMenuInterchangeMutation(mutation))
}

func TestMenuInterchangeValidatesManifestAndReportsOnlyChangedImportedUnits(t *testing.T) {
	t.Parallel()
	plan := &translation.ExtractionPlan{
		EntityType: "menu", EntityID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
		Units: []translation.Unit{
			{UnitID: "item:home:label", ContainerType: translation.ContainerTypeBlock, ContainerID: "home", FieldName: "label"},
			{UnitID: "item:about:label", ContainerType: translation.ContainerTypeBlock, ContainerID: "about", FieldName: "label"},
		},
	}
	mutation := TranslationInterchangeMutation{
		MenuID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Plan: plan,
		Targets: map[string]translation.UnitResult{
			"item:home:label":  {UnitID: "item:home:label", TranslatedText: "Home"},
			"item:about:label": {UnitID: "item:about:label", TranslatedText: "About us"},
		},
		UnitHandles: []string{"item:about:label", "item:home:label"},
	}
	require.NoError(t, validateMenuInterchangeMutation(mutation))
	require.Equal(t, []string{"item:about:label"}, changedMenuInterchangeHandles(
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		map[string]translation.UnitResult{
			"item:home:label":  {UnitID: "item:home:label", TranslatedText: "Home"},
			"item:about:label": {UnitID: "item:about:label", TranslatedText: "About"},
		},
		mutation.Targets,
		mutation.UnitHandles,
	))
	require.Equal(t, []string{"item:about:label"}, changedMenuInterchangeHandles(
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		map[string]translation.UnitResult{
			"item:home:label":  {UnitID: "item:home:label", TranslatedText: "Home"},
			"item:about:label": {UnitID: "item:about:label", TranslatedText: "About"},
		},
		map[string]translation.UnitResult{
			"item:home:label": {UnitID: "item:home:label", TranslatedText: "Home"},
		},
		[]string{"item:home:label"},
	), "REPLACE must report the existing target unit it removed")

	mutation.UnitHandles = mutation.UnitHandles[:1]
	require.Error(t, validateMenuInterchangeMutation(mutation))
}
