package form

import (
	"encoding/json"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestFormInterchangePreservesSparseEmptyValuesAndSourceTopology(t *testing.T) {
	t.Parallel()
	formID := "019c89aa-6798-7a37-8532-11e03f729c35"
	source := &translation.SourceDocument{
		Title: "Contact",
		ContentJSON: []byte(`{
			"id":"schema","steps":[{"id":"step-a","title":"Contact details","fields":[
				{"id":"email","key":"email","type":"email","label":"Email"},
				{"id":"name","key":"name","type":"text","label":"Name"}
			]}]
		}`),
	}
	plan, err := BuildTranslationExtractionPlan(formID, "en", "ko", source)
	require.NoError(t, err)
	empty := ""
	row := formAIDocumentLocaleRow{
		Title: &empty,
		Schema: []byte(`{
			"id":"schema","steps":[{"id":"step-a","title":"","fields":[
				{"id":"email","key":"email","type":"email"},
				{"id":"name","key":"name","type":"text"}
			]}]
		}`),
	}
	current, err := projectFormInterchangeTargets(plan, row)
	require.NoError(t, err)
	require.Contains(t, current, "entity:title")
	require.Empty(t, current["entity:title"].TranslatedText)
	require.Contains(t, current, "step:step-a:title")
	require.Empty(t, current["step:step-a:title"].TranslatedText)
	require.NotContains(t, current, "field:email:label")

	desired := mergeFormInterchangeTargets(
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		current,
		map[string]translation.UnitResult{
			"field:email:label": {UnitID: "field:email:label", TranslatedText: "이메일"},
		},
	)
	candidate, err := ApplyTranslationCandidate(source, desired)
	require.NoError(t, err)
	require.NoError(t, validateFormAIDocumentTargetSchema(source.ContentJSON, candidate.ContentJSON))
	var schema formDocumentObject
	require.NoError(t, json.Unmarshal(candidate.ContentJSON, &schema))
	steps := formValueSlice(schema["steps"])
	require.Len(t, steps, 1)
	step, ok := steps[0].(formDocumentObject)
	require.True(t, ok)
	require.Equal(t, "", step["title"])
	fields := formValueSlice(step["fields"])
	require.Len(t, fields, 2)
	firstField, ok := fields[0].(formDocumentObject)
	require.True(t, ok)
	secondField, ok := fields[1].(formDocumentObject)
	require.True(t, ok)
	require.Equal(t, "email", firstField["key"], "source-owned structure must remain unchanged")
	require.Equal(t, "이메일", firstField["label"])
	_, present := secondField["label"]
	require.False(t, present, "an absent target unit must remain absent")
}

func TestFormInterchangeDoesNotMaterializeSchemaForTitleOnlyPatch(t *testing.T) {
	t.Parallel()
	source := &translation.SourceDocument{
		Title:       "Contact",
		ContentJSON: []byte(`{"steps":[{"id":"step-a","title":"Step","fields":[]}]}`),
	}
	plan, err := BuildTranslationExtractionPlan(
		"019c89aa-6798-7a37-8532-11e03f729c35", "en", "ko", source,
	)
	require.NoError(t, err)
	desired := map[string]translation.UnitResult{
		"entity:title": {UnitID: "entity:title", TranslatedText: "문의"},
	}
	candidate, err := ApplyTranslationCandidate(source, desired)
	require.NoError(t, err)
	if !formInterchangeHasSchemaTarget(plan, desired) {
		candidate.ContentJSON = nil
		candidate.ContentText = nil
	}
	require.Nil(t, candidate.ContentJSON)
	require.Nil(t, candidate.ContentText)
}

func TestFormInterchangeValidatesManifestAndReportsOnlyChangedImportedUnits(t *testing.T) {
	t.Parallel()
	formID := "019c89aa-6798-7a37-8532-11e03f729c35"
	plan := &translation.ExtractionPlan{
		EntityType: "form", EntityID: formID, SourceLocale: "en", TargetLocale: "ko",
		Units: []translation.Unit{
			{UnitID: "entity:title", ContainerType: translation.ContainerTypeEntity, ContainerID: formID, FieldName: "title"},
			{UnitID: "step:step-a:title", ContainerType: translation.ContainerTypeSection, ContainerID: "step-a", FieldName: "content_json"},
		},
	}
	mutation := TranslationInterchangeMutation{
		FormID: formID, SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: &translation.SourceDocument{}, Plan: plan,
		Targets: map[string]translation.UnitResult{
			"entity:title":      {UnitID: "entity:title", TranslatedText: "문의"},
			"step:step-a:title": {UnitID: "step:step-a:title", TranslatedText: "새 단계"},
		},
		UnitHandles: []string{"step:step-a:title", "entity:title"},
	}
	require.NoError(t, validateFormInterchangeMutation(mutation))
	require.Equal(t, []string{"step:step-a:title"}, changedFormInterchangeHandles(
		map[string]translation.UnitResult{
			"entity:title":      {UnitID: "entity:title", TranslatedText: "문의"},
			"step:step-a:title": {UnitID: "step:step-a:title", TranslatedText: "이전 단계"},
		},
		mutation.Targets,
		mutation.UnitHandles,
	))

	mutation.UnitHandles = append(mutation.UnitHandles, "entity:title")
	require.Error(t, validateFormInterchangeMutation(mutation))
}
