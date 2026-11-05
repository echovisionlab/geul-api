package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationExtractionPlanFormUsesStructuredSchemaUnits(t *testing.T) {
	t.Parallel()

	job := PlanTarget{EntityType: "form", EntityID: "form-1", SourceLocale: "en", TargetLocale: "ko"}
	plan, err := testTranslationDomains{}.BuildExtractionPlan(job, &translation.SourceDocument{
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

func TestBuildEntityTranslationBundlesSplitsOversizedContainer(t *testing.T) {
	t.Parallel()

	job := PlanTarget{EntityType: "form", EntityID: "form-large", SourceLocale: "en", TargetLocale: "ko"}
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

	job := PlanTarget{EntityType: "form", EntityID: "form-large-text", SourceLocale: "en", TargetLocale: "ko"}
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

	_, err := testTranslationDomains{}.BuildExtractionPlan(PlanTarget{
		EntityType: "form", EntityID: "form-1", SourceLocale: "en", TargetLocale: "ja",
	}, &translation.SourceDocument{})
	require.Error(t, err)
	require.True(t, errors.Is(err, translation.ErrNoTranslatableUnits))
}
