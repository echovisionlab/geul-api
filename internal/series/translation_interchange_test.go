package series

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostSeriesInterchangePreservesAbsentAndExplicitEmptyTargets(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	plan := &translation.ExtractionPlan{
		EntityType: "series", EntityID: seriesID, SourceLocale: "en", TargetLocale: "ko",
		Units: []translation.Unit{
			{UnitID: "entity:title", ContainerType: translation.ContainerTypeEntity, ContainerID: seriesID, FieldName: "title"},
			{UnitID: "entity:summary", ContainerType: translation.ContainerTypeEntity, ContainerID: seriesID, FieldName: "summary"},
		},
	}
	empty := ""
	current := projectSeriesInterchangeTargets(plan, Translation{Title: &empty}, true)
	require.Contains(t, current, "entity:title")
	require.Empty(t, current["entity:title"].TranslatedText)
	require.NotContains(t, current, "entity:summary")

	desired := mergeSeriesInterchangeTargets(
		InterchangeModePatch,
		current,
		map[string]translation.UnitResult{
			"entity:summary": {UnitID: "entity:summary", TranslatedText: "요약"},
		},
	)
	require.NotNil(t, seriesInterchangeTarget(desired, plan, "title"))
	require.Empty(t, *seriesInterchangeTarget(desired, plan, "title"))
	require.Equal(t, "요약", *seriesInterchangeTarget(desired, plan, "summary"))
}

func TestPostSeriesInterchangeRevisionUsesTargetFactsOnly(t *testing.T) {
	t.Parallel()
	updatedAt := time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, LocaleUpdatedAt: &updatedAt,
	})
	require.NoError(t, err)
	require.NotEmpty(t, revision)
}

func TestPostSeriesInterchangeRejectsStructuralUnits(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	mutation := TranslationInterchangeMutation{
		SeriesID: seriesID, SourceLocale: "en", TargetLocale: "ko",
		Mode: InterchangeModePatch,
		Plan: &translation.ExtractionPlan{
			EntityType: "series", EntityID: seriesID, SourceLocale: "en", TargetLocale: "ko",
			Units: []translation.Unit{{
				UnitID: "relation:posts", ContainerType: translation.ContainerTypeRelation,
				ContainerID: seriesID, FieldName: "posts",
			}},
		},
		Targets: map[string]translation.UnitResult{
			"relation:posts": {UnitID: "relation:posts", TranslatedText: "changed"},
		},
	}
	require.Error(t, validateSeriesInterchangeMutation(mutation))
}

func TestPostSeriesInterchangeValidatesManifestAndReportsOnlyChangedImportedUnits(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	plan := &translation.ExtractionPlan{
		EntityType: "series", EntityID: seriesID, SourceLocale: "en", TargetLocale: "ko",
		Units: []translation.Unit{
			{UnitID: "entity:title", ContainerType: translation.ContainerTypeEntity, ContainerID: seriesID, FieldName: "title"},
			{UnitID: "entity:summary", ContainerType: translation.ContainerTypeEntity, ContainerID: seriesID, FieldName: "summary"},
		},
	}
	mutation := TranslationInterchangeMutation{
		SeriesID: seriesID, SourceLocale: "en", TargetLocale: "ko",
		Mode: InterchangeModePatch,
		Plan: plan,
		Targets: map[string]translation.UnitResult{
			"entity:title":   {UnitID: "entity:title", TranslatedText: "제목"},
			"entity:summary": {UnitID: "entity:summary", TranslatedText: "새 요약"},
		},
		UnitHandles: []string{"entity:summary", "entity:title"},
	}
	require.NoError(t, validateSeriesInterchangeMutation(mutation))
	require.Equal(t, []string{"entity:summary"}, changedSeriesInterchangeHandles(
		map[string]translation.UnitResult{
			"entity:title":   {UnitID: "entity:title", TranslatedText: "제목"},
			"entity:summary": {UnitID: "entity:summary", TranslatedText: "이전 요약"},
		},
		mutation.Targets,
		mutation.UnitHandles,
	))

	mutation.UnitHandles = mutation.UnitHandles[:1]
	require.Error(t, validateSeriesInterchangeMutation(mutation))
}
