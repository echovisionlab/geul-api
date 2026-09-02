package email

import (
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func canonicalLayoutSourceForTest(t *testing.T, source string) string {
	t.Helper()
	canonical, err := CanonicalizeLayoutSourceMarkers(source)
	require.NoError(t, err)
	return canonical
}

func TestLayoutTranslationHTMLPreservesWrapperAndStableUnit(t *testing.T) {
	t.Parallel()

	raw := `<!DOCTYPE html><html><head><style>body { color: red; }</style></head><body><main>{{content}}</main><footer> Unsubscribe </footer></body></html>`
	source := canonicalLayoutSourceForTest(t, raw)
	recanonical, err := CanonicalizeLayoutSourceMarkers(source)
	require.NoError(t, err)
	assert.Equal(t, source, recanonical)

	plan, err := BuildLayoutTranslationExtractionPlan("11111111-1111-4111-8111-111111111111", "en", "ko", &translation.SourceDocument{ContentHTML: &source})
	require.NoError(t, err)
	require.Len(t, plan.Units, 1)
	assert.Regexp(t, `^unit:[0-9a-f-]{36}:text$`, plan.Units[0].UnitID)
	assert.Equal(t, " Unsubscribe ", plan.Units[0].SourceText)

	result, textResult, err := ApplyLayoutHTMLTranslationCandidate(source, map[string]translation.UnitResult{
		plan.Units[0].UnitID: {UnitID: plan.Units[0].UnitID, TranslatedText: "구독 해지"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, *result, `<main>{{content}}</main>`)
	assert.Contains(t, *result, `<style>body { color: red; }</style>`)
	assert.Contains(t, *result, `<!--geul-value:text:`)
	require.NotNil(t, textResult)
	assert.Equal(t, "구독 해지", *textResult)

	rendered, err := ResolveLayoutLocaleMarkup(source, result)
	require.NoError(t, err)
	assert.Contains(t, rendered, `<footer> 구독 해지 </footer>`)
	assert.NotContains(t, rendered, "geul-unit")
	assert.NotContains(t, rendered, "geul-value")
}

func TestLayoutTranslationExplicitEmptyDiffersFromAbsent(t *testing.T) {
	t.Parallel()

	source := canonicalLayoutSourceForTest(t, `<main title=" Source title ">{{content}}</main><footer> Source footer </footer>`)
	units, err := ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)
	textHandle := ""
	attributeHandle := ""
	for _, unit := range units {
		if unit.Attribute == "" {
			textHandle = unit.Handle
		} else {
			attributeHandle = unit.Handle
		}
	}
	require.NotEmpty(t, textHandle)
	require.NotEmpty(t, attributeHandle)

	absent, _, err := ApplyLayoutLocaleValues(source, nil)
	require.NoError(t, err)
	absentValues, err := ExtractLayoutLocaleValues(*absent)
	require.NoError(t, err)
	assert.NotContains(t, absentValues, textHandle)
	absentRender, err := ResolveLayoutLocaleMarkup(source, absent)
	require.NoError(t, err)
	assert.Contains(t, absentRender, "Source footer")

	empty, _, err := ApplyLayoutLocaleValues(source, map[string]string{textHandle: ""})
	require.NoError(t, err)
	emptyValues, err := ExtractLayoutLocaleValues(*empty)
	require.NoError(t, err)
	require.Contains(t, emptyValues, textHandle)
	assert.Equal(t, "", emptyValues[textHandle])
	emptyRender, err := ResolveLayoutLocaleMarkup(source, empty)
	require.NoError(t, err)
	assert.NotContains(t, emptyRender, "Source footer")

	emptyAttribute, _, err := ApplyLayoutLocaleValues(source, map[string]string{attributeHandle: ""})
	require.NoError(t, err)
	emptyAttributeValues, err := ExtractLayoutLocaleValues(*emptyAttribute)
	require.NoError(t, err)
	assert.Equal(t, "", emptyAttributeValues[attributeHandle])
	emptyAttributeRender, err := ResolveLayoutLocaleMarkup(source, emptyAttribute)
	require.NoError(t, err)
	assert.Contains(t, emptyAttributeRender, `title=""`)
}

func TestLayoutRoleNeutralSourceMaterializationPreservesEmptyUnitsAndStableIDs(t *testing.T) {
	t.Parallel()

	source := canonicalLayoutSourceForTest(
		t,
		`<main title="Source title"><h1>First</h1><p>Second</p>{{content}}</main>`,
	)
	sourceUnits, err := ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, sourceUnits, 3)
	values := make(map[string]string, len(sourceUnits))
	for _, unit := range sourceUnits {
		values[unit.Handle] = ""
	}
	overlay, _, err := ApplyLayoutLocaleValues(source, values)
	require.NoError(t, err)
	extracted, err := ExtractLayoutLocaleValues(*overlay)
	require.NoError(t, err)
	require.Equal(t, values, extracted)

	materialized, _, err := MaterializeLayoutSourceFromLocale(source, overlay)
	require.NoError(t, err)
	materializedUnits, err := ExtractLayoutContentUnits(*materialized)
	require.NoError(t, err)
	require.Len(t, materializedUnits, len(sourceUnits))
	for index := range sourceUnits {
		assert.Equal(t, sourceUnits[index].Handle, materializedUnits[index].Handle)
		assert.Empty(t, materializedUnits[index].SourceValue)
	}
	recanonical, err := CanonicalizeLayoutSourceMarkers(*materialized)
	require.NoError(t, err)
	assert.Equal(t, *materialized, recanonical)

	normalized, _, err := NormalizeLayoutLocaleRepresentation(*materialized, *overlay)
	require.NoError(t, err)
	normalizedValues, err := ExtractLayoutLocaleValues(*normalized)
	require.NoError(t, err)
	assert.Equal(t, values, normalizedValues)
}

func TestLayoutRoleNeutralMaterializationKeepsTargetAbsenceAcrossRoleChanges(t *testing.T) {
	t.Parallel()

	source := canonicalLayoutSourceForTest(t, `<main><h1>First</h1><p>Second</p>{{content}}</main>`)
	units, err := ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)
	targetValues := map[string]string{units[1].Handle: "번역"}
	overlay, _, err := ApplyLayoutLocaleValues(source, targetValues)
	require.NoError(t, err)

	materialized, _, err := MaterializeLayoutSourceFromLocale(source, overlay)
	require.NoError(t, err)
	materializedUnits, err := ExtractLayoutContentUnits(*materialized)
	require.NoError(t, err)
	assert.Empty(t, materializedUnits[0].SourceValue)
	assert.Equal(t, "번역", materializedUnits[1].SourceValue)

	storedValues, err := ExtractLayoutStoredLocaleValues(*overlay)
	require.NoError(t, err)
	assert.Equal(t, targetValues, storedValues)
	assert.NotContains(t, storedValues, units[0].Handle)
}

func TestLayoutTranslationExtractionUsesSharedBundleLimit(t *testing.T) {
	t.Parallel()

	source := canonicalLayoutSourceForTest(t, "<main>"+strings.Repeat("a", 7000)+"</main><footer>"+strings.Repeat("b", 7000)+"</footer>")
	plan, err := BuildLayoutTranslationExtractionPlan("11111111-1111-4111-8111-111111111111", "en", "ko", &translation.SourceDocument{ContentHTML: &source})
	require.NoError(t, err)
	require.Len(t, plan.Bundles, 2)
	assert.Equal(t, "html:main", plan.Bundles[0].BundleID)
	assert.Equal(t, "html:main:chunk:2", plan.Bundles[1].BundleID)
}

func TestLayoutTranslationHTMLIncludesVisibleAccessibilityAttributes(t *testing.T) {
	t.Parallel()

	raw := `<!DOCTYPE html><html><head><style>.hero { color: red; }</style></head><body><main title=" Main title "><img src="https://cdn.example/hero.png?x=1&amp;y=2" alt="Hero {{name}}" data-label="Never translate"><button aria-label="Unsubscribe action"> Unsubscribe </button><span title="{{title}}">{{content}}</span><script>const title = "Never translate";</script></main></body></html>`
	source := canonicalLayoutSourceForTest(t, raw)
	plan, err := BuildLayoutTranslationExtractionPlan("11111111-1111-4111-8111-111111111111", "en", "ko", &translation.SourceDocument{ContentHTML: &source})
	require.NoError(t, err)

	bySource := make(map[string]translation.Unit, len(plan.Units))
	for _, unit := range plan.Units {
		bySource[strings.TrimSpace(unit.SourceText)] = unit
	}
	for _, expected := range []string{"Main title", "Hero {{name}}", "Unsubscribe action", "Unsubscribe"} {
		require.Contains(t, bySource, expected)
		assert.Contains(t, bySource[expected].UnitID, "unit:")
	}
	assert.NotContains(t, bySource, "{{title}}")
	assert.NotContains(t, bySource, "{{content}}")
	assert.NotContains(t, bySource, "Never translate")

	results := map[string]translation.UnitResult{
		bySource["Main title"].UnitID:         {TranslatedText: "기본 제목"},
		bySource["Hero {{name}}"].UnitID:      {TranslatedText: "영웅 {{name}}"},
		bySource["Unsubscribe action"].UnitID: {TranslatedText: "구독 해지 동작"},
		bySource["Unsubscribe"].UnitID:        {TranslatedText: "구독 해지"},
	}
	target, _, err := ApplyLayoutHTMLTranslationCandidate(source, results)
	require.NoError(t, err)
	rendered, err := ResolveLayoutLocaleMarkup(source, target)
	require.NoError(t, err)
	assert.Contains(t, rendered, `title=" 기본 제목 "`)
	assert.Contains(t, rendered, `alt="영웅 {{name}}"`)
	assert.Contains(t, rendered, `aria-label="구독 해지 동작"`)
	assert.Contains(t, rendered, `> 구독 해지 </button>`)
	assert.Contains(t, rendered, `src="https://cdn.example/hero.png?x=1&amp;y=2"`)
	assert.Contains(t, rendered, `data-label="Never translate"`)
	assert.Contains(t, rendered, `<style>.hero { color: red; }</style>`)
	assert.Contains(t, rendered, `<script>const title = "Never translate";</script>`)
	assert.Contains(t, rendered, `title="{{title}}"`)
	assert.Contains(t, rendered, `>{{content}}</span>`)
}

func TestLayoutTranslationRejectsUnmarkedAndDuplicateStableUnits(t *testing.T) {
	t.Parallel()

	raw := `<main>{{content}}</main><footer>Source footer</footer>`
	_, err := BuildLayoutTranslationExtractionPlan("11111111-1111-4111-8111-111111111111", "en", "ko", &translation.SourceDocument{ContentHTML: &raw})
	require.ErrorContains(t, err, "missing its durable unit marker")

	duplicate := `<!--geul-unit:text:11111111-1111-4111-8111-111111111111-->One<!--geul-unit:text:11111111-1111-4111-8111-111111111111-->Two`
	_, err = ExtractLayoutContentUnits(duplicate)
	require.ErrorContains(t, err, "duplicate")
}
