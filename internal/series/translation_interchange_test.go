package series

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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
	current := projectSeriesInterchangeTargets(plan, model.SeriesTranslation{Title: &empty}, true)
	require.Contains(t, current, "entity:title")
	require.Empty(t, current["entity:title"].TranslatedText)
	require.NotContains(t, current, "entity:summary")

	desired := mergeSeriesInterchangeTargets(
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
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

func TestPostSeriesInterchangeTargetRevisionIncludesSharedDocumentRevision(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE content_document (id TEXT PRIMARY KEY, profile TEXT NOT NULL, revision TEXT NOT NULL)`,
		`CREATE TABLE series (id TEXT PRIMARY KEY, source_locale TEXT NOT NULL, content_document_id TEXT NOT NULL)`,
		`CREATE TABLE series_translation (entity_id TEXT NOT NULL, locale TEXT NOT NULL, title TEXT, updated_at DATETIME NOT NULL, PRIMARY KEY (entity_id, locale))`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	seriesID := uuid.NewString()
	documentID := uuid.NewString()
	firstDocumentRevision := uuid.NewString()
	secondDocumentRevision := uuid.NewString()
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, 'compact', ?)`,
		documentID, firstDocumentRevision,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO series (id, source_locale, content_document_id) VALUES (?, 'en', ?)`,
		seriesID, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO series_translation (entity_id, locale, title, updated_at) VALUES (?, 'ko', 'Korean', ?)`,
		seriesID, now,
	).Error)

	before, err := loadSeriesInterchangeState(t.Context(), db, seriesID, "ko", false)
	require.NoError(t, err)
	require.NotEmpty(t, before.revision)
	require.NoError(t, db.Exec(
		`UPDATE content_document SET revision = ? WHERE id = ?`, secondDocumentRevision, documentID,
	).Error)
	after, err := loadSeriesInterchangeState(t.Context(), db, seriesID, "ko", false)
	require.NoError(t, err)
	require.NotEqual(t, before.revision, after.revision)
}

func TestPostSeriesInterchangeRejectsStructuralUnits(t *testing.T) {
	t.Parallel()
	seriesID := uuid.NewString()
	mutation := TranslationInterchangeMutation{
		SeriesID: seriesID, SourceLocale: "en", TargetLocale: "ko",
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
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
		Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
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
