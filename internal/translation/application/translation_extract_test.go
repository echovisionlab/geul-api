package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestBuildTranslationExtractionPlanFormUsesStructuredSchemaUnits(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "form", EntityID: "form-1", SourceLocale: "en", TargetLocale: "ko"}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, &translation.SourceDocument{
		Title: "Contact form",
		ContentJSON: []byte(`{
			"id":"schema-1",
			"steps":[{"id":"step-1","title":"Contact","description":"Help us route your request","fields":[
				{"id":"field-email","key":"email","label":"Email address","description":"We will reply here","placeholder":"name@example.com","type":"email","validation":{"validators":[{"id":"validator-required","message":"Email is required"}]}},
				{"id":"field-topic","key":"topic","label":"Topic","type":"select","options":[{"id":"option-billing","value":"billing","label":"Billing"}]}
			]}]
		}`),
	})
	require.NoError(t, err)

	unitIDs := make([]string, 0, len(plan.Units))
	for _, unit := range plan.Units {
		unitIDs = append(unitIDs, unit.UnitID)
	}
	assert.Contains(t, unitIDs, "entity:title")
	assert.Contains(t, unitIDs, "step:step-1:title")
	assert.Contains(t, unitIDs, "step:step-1:description")
	assert.Contains(t, unitIDs, "field:field-email:label")
	assert.Contains(t, unitIDs, "field:field-email:description")
	assert.Contains(t, unitIDs, "field:field-email:placeholder")
	assert.Contains(t, unitIDs, "field:field-email:validator:validator-required:message")
	assert.Contains(t, unitIDs, "field:field-topic:option:option-billing:label")
	assert.NotContains(t, unitIDs, "field:field-email:key")
}

func TestBuildTranslationCandidateContentAppliesFormSchemaTranslations(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "form", EntityID: "form-1", SourceLocale: "en", TargetLocale: "ko"}
	source := &translation.SourceDocument{
		Title: "Contact form",
		ContentJSON: []byte(`{
			"id":"schema-1",
			"steps":[{"id":"step-1","title":"Contact","description":"Help us route your request","fields":[
				{"id":"field-email","key":"email","label":"Email address","description":"We will reply here","placeholder":"name@example.com","type":"email","validation":{"validators":[{"id":"validator-required","message":"Email is required"}]}},
				{"id":"field-topic","key":"topic","label":"Topic","type":"select","options":[{"id":"option-billing","value":"billing","label":"Billing"}]}
			]}]
		}`),
	}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, source)
	require.NoError(t, err)

	candidate, err := buildTranslationCandidateContent(testTranslationDomains{}, plan, source, translationProviderResponseForPlanTest(t, plan,
		translation.UnitResult{UnitID: "entity:title", TranslatedText: "문의 폼"},
		translation.UnitResult{UnitID: "step:step-1:title", TranslatedText: "문의"},
		translation.UnitResult{UnitID: "step:step-1:description", TranslatedText: "문의 내용을 올바르게 분류하는 데 도움이 됩니다"},
		translation.UnitResult{UnitID: "field:field-email:label", TranslatedText: "이메일 주소"},
		translation.UnitResult{UnitID: "field:field-email:description", TranslatedText: "이 주소로 답변드립니다"},
		translation.UnitResult{UnitID: "field:field-email:placeholder", TranslatedText: "name@example.com"},
		translation.UnitResult{UnitID: "field:field-email:validator:validator-required:message", TranslatedText: "이메일 주소를 입력해주세요"},
		translation.UnitResult{UnitID: "field:field-topic:label", TranslatedText: "주제"},
		translation.UnitResult{UnitID: "field:field-topic:option:option-billing:label", TranslatedText: "결제"},
	))
	require.NoError(t, err)
	require.NotNil(t, candidate.Title)
	assert.Equal(t, "문의 폼", *candidate.Title)
	require.NotNil(t, candidate.ContentText)
	assert.Equal(t, "문의\n문의 내용을 올바르게 분류하는 데 도움이 됩니다\n이메일 주소\n이 주소로 답변드립니다\nname@example.com\n이메일 주소를 입력해주세요\n주제\n결제", *candidate.ContentText)
	assert.JSONEq(t, `{
		"id":"schema-1",
		"steps":[{"id":"step-1","title":"문의","description":"문의 내용을 올바르게 분류하는 데 도움이 됩니다","fields":[
			{"id":"field-email","key":"email","label":"이메일 주소","description":"이 주소로 답변드립니다","placeholder":"name@example.com","type":"email","validation":{"validators":[{"id":"validator-required","message":"이메일 주소를 입력해주세요"}]}},
			{"id":"field-topic","key":"topic","label":"주제","type":"select","options":[{"id":"option-billing","value":"billing","label":"결제"}]}
		]}]
	}`, string(candidate.ContentJSON))
}

func TestBuildEntityTranslationBundlesSplitsOversizedContainer(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "form", EntityID: "form-large", SourceLocale: "en", TargetLocale: "ko"}
	units := make([]translation.Unit, 0, translation.MaxUnitsPerProviderBundle+1)
	for index := range translation.MaxUnitsPerProviderBundle + 1 {
		units = append(units, translation.Unit{
			UnitID: "block:block-a:text:" + string(rune('a'+index%26)), EntityType: job.EntityType,
			EntityID: job.EntityID, ContainerType: translation.ContainerTypeBlock, ContainerID: "block-a",
			FieldName: "text", SourceText: "Paragraph", SourceFormat: translation.SourceFormatPlainText,
			SourceLocale: job.SourceLocale,
		})
	}
	bundles := translation.BuildBundles(
		job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, units, nil,
	)
	require.Len(t, bundles, 2)
	assert.Equal(t, "block:block-a", bundles[0].BundleID)
	assert.Equal(t, "block:block-a:chunk:2", bundles[1].BundleID)
	assert.Len(t, bundles[0].Units, translation.MaxUnitsPerProviderBundle)
	assert.Len(t, bundles[1].Units, 1)
}

func TestBuildEntityTranslationBundlesSplitsOversizedSourceBytes(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "form", EntityID: "form-large-text", SourceLocale: "en", TargetLocale: "ko"}
	units := []translation.Unit{
		{UnitID: "block:block-a:text:0", EntityType: job.EntityType, EntityID: job.EntityID, ContainerType: translation.ContainerTypeBlock, ContainerID: "block-a", FieldName: "text", SourceText: strings.Repeat("a", translation.MaxSourceBytesPerProviderBundle), SourceFormat: translation.SourceFormatPlainText, SourceLocale: job.SourceLocale},
		{UnitID: "block:block-a:text:1", EntityType: job.EntityType, EntityID: job.EntityID, ContainerType: translation.ContainerTypeBlock, ContainerID: "block-a", FieldName: "text", SourceText: "next paragraph", SourceFormat: translation.SourceFormatPlainText, SourceLocale: job.SourceLocale},
	}
	bundles := translation.BuildBundles(
		job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, units, nil,
	)
	require.Len(t, bundles, 2)
	assert.Len(t, bundles[0].Units, 1)
	assert.Len(t, bundles[1].Units, 1)
}

func TestBuildTranslationExtractionPlanRejectsEmptyLegacySource(t *testing.T) {
	t.Parallel()

	_, err := buildTranslationExtractionPlan(testTranslationDomains{}, &model.TranslationJob{
		EntityType: "form", EntityID: "form-1", SourceLocale: "en", TargetLocale: "ja",
	}, &translation.SourceDocument{})
	require.Error(t, err)
	require.True(t, errors.Is(err, translation.ErrNoTranslatableUnits))
}

func TestBuildTranslationCandidateContentUsesFieldMapping(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "series", EntityID: "series-1", SourceLocale: "en", TargetLocale: "fr"}
	source := &translation.SourceDocument{Title: "Original title", Summary: translationJobStringPtr("Original summary"), ContentText: translationJobStringPtr("Original body")}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, source)
	require.NoError(t, err)
	candidate, err := buildTranslationCandidateContent(testTranslationDomains{}, plan, source, translationProviderResponseForPlanTest(t, plan,
		translation.UnitResult{UnitID: "entity:title", TranslatedText: "Titre"},
		translation.UnitResult{UnitID: "entity:summary", TranslatedText: "Resume"},
		translation.UnitResult{UnitID: "entity:content_text", TranslatedText: "Corps"},
	))
	require.NoError(t, err)
	require.NotNil(t, candidate.Title)
	require.NotNil(t, candidate.Summary)
	require.NotNil(t, candidate.ContentText)
	assert.Equal(t, "Titre", *candidate.Title)
	assert.Equal(t, "Resume", *candidate.Summary)
	assert.Equal(t, "Corps", *candidate.ContentText)
}

func TestBuildTranslationCandidateContentPreservesEmailLayoutWrapperMarkup(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{EntityType: "email_layout", EntityID: "layout-1", SourceLocale: "ko", TargetLocale: "en"}
	sourceHTML, err := email.CanonicalizeLayoutSourceMarkers(`<!DOCTYPE html>
<html lang="{{email_lang}}" dir="{{email_direction}}">
<head>
  <meta charset="utf-8">
  <style>
    body, table {
      font-family: {{email_font_family}} !important;
    }
  </style>
</head>
<body>
  <main>
    {{content}}
  </main>
  <footer>
    <a href="{{unsubscribe_link}}">구독 해지</a>
  </footer>
</body>
</html>`)
	require.NoError(t, err)
	source := &translation.SourceDocument{ContentHTML: translationJobStringPtr(sourceHTML)}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 1)
	candidate, err := buildTranslationCandidateContent(testTranslationDomains{},
		plan,
		source,
		translationProviderResponseForPlanTest(
			t,
			plan,
			translation.UnitResult{UnitID: plan.Units[0].UnitID, TranslatedText: "Unsubscribe"},
		),
	)
	require.NoError(t, err)
	require.NotNil(t, candidate.ContentHTML)
	require.NotNil(t, candidate.ContentText)
	assert.Contains(t, *candidate.ContentHTML, `<!DOCTYPE html>`)
	assert.Contains(t, *candidate.ContentHTML, `font-family: {{email_font_family}} !important;`)
	assert.Contains(t, *candidate.ContentHTML, `{{content}}`)
	assert.Contains(t, *candidate.ContentHTML, `href="{{unsubscribe_link}}"`)
	assert.Contains(t, *candidate.ContentHTML, `<!--geul-unit:text:`)
	assert.Contains(t, *candidate.ContentHTML, `<!--geul-value:text:`)
	assert.Equal(t, "Unsubscribe", *candidate.ContentText)

	rendered, err := email.ResolveLayoutLocaleMarkup(sourceHTML, candidate.ContentHTML)
	require.NoError(t, err)
	assert.Contains(t, rendered, `href="{{unsubscribe_link}}">Unsubscribe</a>`)
	assert.NotContains(t, rendered, `geul-unit`)
	assert.NotContains(t, rendered, `geul-value`)
}
