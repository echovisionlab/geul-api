package series

import (
	"errors"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationExtractionPlan(t *testing.T) {
	t.Parallel()

	summary := "Quiet summary"
	contentText := "Body text"
	plan, err := BuildTranslationExtractionPlan("series-1", "en", "ko", &translation.SourceDocument{
		Title:          " Hello ",
		Summary:        &summary,
		ContentText:    &contentText,
		ProtectedTerms: []string{" Geul ", ""},
	})
	require.NoError(t, err)

	assert.Equal(t, "series", plan.EntityType)
	assert.Equal(t, "series-1", plan.EntityID)
	assert.Equal(t, "en", plan.SourceLocale)
	assert.Equal(t, "ko", plan.TargetLocale)
	require.NotNil(t, plan.ContextTitle)
	assert.Equal(t, "Hello", *plan.ContextTitle)
	assert.Equal(t, []string{"Geul"}, plan.ProtectedTerms)
	require.Len(t, plan.Units, 3)
	assert.Equal(t, []string{"entity:title", "entity:summary", "entity:content_text"}, []string{
		plan.Units[0].UnitID,
		plan.Units[1].UnitID,
		plan.Units[2].UnitID,
	})
	assert.Equal(t, "Hello", plan.Units[0].SourceText)
	require.Len(t, plan.Bundles, 1)
	assert.Equal(t, "entity:meta", plan.Bundles[0].BundleID)
	assert.Equal(t, translation.BundleTypeEntity, plan.Bundles[0].BundleType)
	assert.Equal(t, 0, plan.Bundles[0].SequenceIndex)
	assert.Equal(t, 1, plan.Bundles[0].SequenceTotal)
	require.NotNil(t, plan.Bundles[0].ContextText)
	assert.Equal(t, summary, *plan.Bundles[0].ContextText)
}

func TestBuildTranslationExtractionPlanRejectsEmptySource(t *testing.T) {
	t.Parallel()

	_, err := BuildTranslationExtractionPlan("series-1", "en", "ko", &translation.SourceDocument{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, translation.ErrNoTranslatableUnits))
}

func TestBuildTranslationExtractionPlanSplitsLargeSummary(t *testing.T) {
	t.Parallel()

	summary := strings.Repeat("a", 7000)
	plan, err := BuildTranslationExtractionPlan("series-1", "en", "ko", &translation.SourceDocument{
		Title: "Title", Summary: &summary, ContentText: &summary,
	})
	require.NoError(t, err)
	require.Len(t, plan.Bundles, 2)
	assert.Equal(t, "entity:meta", plan.Bundles[0].BundleID)
	assert.Equal(t, "entity:meta:chunk:2", plan.Bundles[1].BundleID)
	assert.Len(t, plan.Bundles[0].Units, 2)
	assert.Len(t, plan.Bundles[1].Units, 1)
}
