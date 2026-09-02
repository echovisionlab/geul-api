package post

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestApplyPostMachineTranslatedMetadataCreatesValuesOnlyTarget(t *testing.T) {
	db := newPostTranslationAdapterDB(t)

	title := "Translated title"
	summary := "Translated summary"
	now := time.Unix(1_700_000_000, 0).UTC()
	changed, err := applyPostTranslatedMetadata(
		t.Context(), db,
		&model.TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &title, Summary: &summary},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.True(t, changed)

	var row struct {
		Title     string
		Summary   string
		CreatedAt time.Time `gorm:"column:created_at"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Table("post_translation").
		Where("entity_id = 'post-1' AND locale = 'ko'").Take(&row).Error)
	require.Equal(t, title, row.Title)
	require.Equal(t, summary, row.Summary)
	require.True(t, row.CreatedAt.Equal(now))
	require.True(t, row.UpdatedAt.Equal(now))
}

func TestApplyPostMachineTranslatedMetadataPreservesExistingRowIdentity(t *testing.T) {
	db := newPostTranslationAdapterDB(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO post_translation (
		 entity_id, locale, title, created_at, updated_at
		) VALUES ('post-1', 'ko', 'old', ?, ?)`,
		now.Add(-time.Hour), now.Add(-time.Hour),
	).Error)

	title := "new"
	changed, err := applyPostTranslatedMetadata(
		t.Context(), db,
		&model.TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &title},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.True(t, changed)

	var row struct {
		Title     string
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	require.NoError(t, db.Table("post_translation").
		Where("entity_id = 'post-1' AND locale = 'ko'").Take(&row).Error)
	require.Equal(t, title, row.Title)
	require.True(t, row.CreatedAt.Equal(now.Add(-time.Hour)))
}

func TestApplyPostMachineTranslatedMetadataPreservesExplicitEmptyTargetTitle(t *testing.T) {
	db := newPostTranslationAdapterDB(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	empty := ""

	changed, err := applyPostTranslatedMetadata(
		t.Context(), db,
		&model.TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &empty},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.True(t, changed)

	var row struct {
		Title *string `gorm:"column:title"`
	}
	require.NoError(t, db.Table("post_translation").
		Where("entity_id = 'post-1' AND locale = 'ko'").Take(&row).Error)
	require.NotNil(t, row.Title, "explicit empty target must not collapse to a missing value")
	require.Empty(t, *row.Title)
}

func TestPostTranslationCandidateValidationKeepsSparseInterchangeScoped(t *testing.T) {
	store := &contentblock.Store{}
	job := &model.TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"}
	candidate := &translation.Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}

	require.Error(t, validatePostTranslationCandidate(
		store, job, candidate, postTranslationCandidateRequireTitle,
	), "AI/provider apply must still require a translated title")
	require.NoError(t, validatePostTranslationCandidate(
		store, job, candidate, postTranslationCandidateAllowSparseInterchange,
	), "XLIFF PATCH may preserve an absent target title")

	empty := ""
	candidate.Title = &empty
	require.NoError(t, validatePostTranslationCandidate(
		store, job, candidate, postTranslationCandidateRequireTitle,
	), "an explicit empty target is present, not missing")
}

func newPostTranslationAdapterDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	for _, statement := range []string{
		`CREATE TABLE post_translation (
		 entity_id TEXT NOT NULL, locale TEXT NOT NULL, title TEXT, summary TEXT,
		 created_at DATETIME, updated_at DATETIME,
		 PRIMARY KEY (entity_id, locale)
		)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}
