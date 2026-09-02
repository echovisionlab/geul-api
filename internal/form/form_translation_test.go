package form

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationExtractionPlanUsesFormSchemaSlots(t *testing.T) {
	t.Parallel()

	summary := " Help us route your request "
	plan, err := BuildTranslationExtractionPlan("form-1", "en", "ko", &translation.SourceDocument{
		Title: " Contact form ", Summary: &summary, ProtectedTerms: []string{" Geul "},
		ContentJSON: []byte(`{"steps":[{"id":"step-1","title":" Contact ","description":" Route us ","fields":[
			{"id":"field-email","type":"email","label":" Email address ","description":" Reply here ","placeholder":"name@example.com","checkboxLabel":" Keep me posted ","options":[{"id":"option-billing","value":"billing","label":" Billing "}],"validation":{"validators":[{"id":"validator-required","message":" Required "}]}}
		]}]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "form", plan.EntityType)
	assert.Equal(t, "form-1", plan.EntityID)
	assert.Equal(t, []string{"Geul"}, plan.ProtectedTerms)
	assert.Equal(t, []string{
		"entity:title",
		"step:step-1:title", "step:step-1:description",
		"field:field-email:label", "field:field-email:description",
		"field:field-email:placeholder", "field:field-email:checkbox_label",
		"field:field-email:option:option-billing:label",
		"field:field-email:validator:validator-required:message",
	}, unitIDs(plan.Units))

	stepTitle := plan.Units[1]
	assert.Equal(t, "schema:step:step-1:title", stepTitle.Path)
	assert.Equal(t, translation.ContainerTypeSection, stepTitle.ContainerType)
	assert.Equal(t, "step-1", stepTitle.ContainerID)
	assert.Nil(t, stepTitle.Context)
	fieldLabel := plan.Units[3]
	assert.Equal(t, "schema:step:step-1:field:field-email:label", fieldLabel.Path)
	assert.Equal(t, translation.ContainerTypeBlock, fieldLabel.ContainerType)
	assert.Equal(t, "field-email", fieldLabel.ContainerID)
	require.NotNil(t, fieldLabel.Context)
	assert.Equal(t, "email", *fieldLabel.Context)
	assert.Equal(t, " Email address ", fieldLabel.SourceText)

	require.Len(t, plan.Bundles, 3)
	assert.Equal(t, "entity:meta", plan.Bundles[0].BundleID)
	assert.Equal(t, "section:step-1", plan.Bundles[1].BundleID)
	assert.Equal(t, "block:field-email", plan.Bundles[2].BundleID)
	assert.Equal(t, 3, plan.Bundles[2].SequenceTotal)
}

func TestApplyTranslationCandidatePreservesFormStructureAndEdgeWhitespace(t *testing.T) {
	t.Parallel()

	source := &translation.SourceDocument{ContentJSON: []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":" Contact ","fields":[
		{"id":"field-email","type":"email","label":" Email ","options":[{"value":"billing","label":" Billing "}],"validation":{"validators":[{"message":" Required "}]}}
	]}]}`)}
	candidate, err := ApplyTranslationCandidate(source, map[string]translation.UnitResult{
		"step:step-1:title":                             {TranslatedText: " 문의 "},
		"field:field-email:label":                       {TranslatedText: " 이메일 "},
		"field:field-email:option:string:billing:label": {TranslatedText: " 결제 "},
		"field:field-email:validator:0:message":         {TranslatedText: " 입력 필요 "},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"step-1","title":" 문의 ","fields":[
		{"id":"field-email","type":"email","label":" 이메일 ","options":[{"value":"billing","label":" 결제 "}],"validation":{"validators":[{"message":" 입력 필요 "}]}}
	]}]}`, string(candidate.ContentJSON))
	require.NotNil(t, candidate.ContentText)
	assert.Equal(t, "문의\n이메일\n결제\n입력 필요", *candidate.ContentText)
}

func TestApplyTranslationCandidateLeavesPostRequestUnitsMissing(t *testing.T) {
	t.Parallel()

	source := &translation.SourceDocument{ContentJSON: []byte(`{"id":"schema-1","steps":[{"id":"requested","title":"Requested","fields":[]},{"id":"added-later","title":"Added later","fields":[]}]}`)}
	candidate, err := ApplyTranslationCandidate(source, map[string]translation.UnitResult{
		"step:requested:title": {TranslatedText: "번역"},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"requested","title":"번역","fields":[]},{"id":"added-later","fields":[]}]}`, string(candidate.ContentJSON))
}

func TestBuildTranslationExtractionPlanRejectsLegacyNonCanonicalBodies(t *testing.T) {
	t.Parallel()

	htmlSource := "<p>Hello {{name}}</p><script>ignored</script>"
	_, err := BuildTranslationExtractionPlan("form-html", "en", "ko", &translation.SourceDocument{ContentHTML: &htmlSource})
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)

	text := "Legacy body"
	_, err = BuildTranslationExtractionPlan("form-text", "en", "ko", &translation.SourceDocument{ContentText: &text})
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)
}

func TestBuildTranslationExtractionPlanRejectsEmptySource(t *testing.T) {
	t.Parallel()

	_, err := BuildTranslationExtractionPlan("form-empty", "en", "ko", &translation.SourceDocument{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, translation.ErrNoTranslatableUnits))
}

func unitIDs(units []translation.Unit) []string {
	ids := make([]string, 0, len(units))
	for _, unit := range units {
		ids = append(ids, unit.UnitID)
	}
	return ids
}
