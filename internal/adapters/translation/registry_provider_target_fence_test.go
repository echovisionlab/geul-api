package translationadapter

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLoadProviderLocaleFenceReturnsOwningRoleAndExactRevisions(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(
		sqlite.Open("file:provider-target-fence-"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE content_document (id TEXT PRIMARY KEY, revision TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE post (
		id TEXT PRIMARY KEY,
		content_document_id TEXT NOT NULL,
		source_locale TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE post_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)

	entityID := uuid.NewString()
	documentID := uuid.NewString()
	documentRevision := uuid.NewString()
	sourceUpdatedAt := time.Unix(1_700_000_300, 123_000).UTC()
	updatedAt := time.Unix(1_700_000_400, 123_000).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, revision) VALUES (?, ?)`,
		documentID,
		documentRevision,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, content_document_id, source_locale) VALUES (?, ?, ?)`,
		entityID,
		documentID,
		"en",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post_translation (entity_id, locale, updated_at) VALUES (?, ?, ?)`,
		entityID,
		"en",
		sourceUpdatedAt,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post_translation (entity_id, locale, updated_at) VALUES (?, ?, ?)`,
		entityID,
		"ko",
		updatedAt,
	).Error)

	fence, err := loadProviderLocaleFence(t.Context(), db, &model.TranslationJob{
		EntityType: "post", EntityID: entityID, TargetLocale: "ko",
	})
	require.NoError(t, err)
	require.True(t, fence.exists)
	require.Equal(t, "en", fence.sourceLocale)
	require.Equal(t, documentRevision, fence.documentRevision)
	expectedTargetRevision, err := core.DeriveTargetRevision(core.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	require.NoError(t, err)
	require.Equal(t, expectedTargetRevision, fence.targetRevision)

	source, err := loadProviderLocaleFence(t.Context(), db, &model.TranslationJob{
		EntityType: "post", EntityID: entityID, TargetLocale: "en",
	})
	require.NoError(t, err)
	require.True(t, source.exists)
	require.Equal(t, "en", source.sourceLocale)
	require.Equal(t, documentRevision, source.documentRevision)
	require.Empty(t, source.targetRevision)
	require.NotNil(t, source.localeUpdatedAt)
	require.True(t, source.localeUpdatedAt.Equal(sourceUpdatedAt))

	missing, err := loadProviderLocaleFence(t.Context(), db, &model.TranslationJob{
		EntityType: "post", EntityID: entityID, TargetLocale: "ja",
	})
	require.NoError(t, err)
	require.False(t, missing.exists)
	require.Equal(t, documentRevision, missing.documentRevision)
	require.Empty(t, missing.targetRevision)
}

func TestClassifyAppliedProviderTranslationUsesCurrentSourceRole(t *testing.T) {
	t.Parallel()

	beforeTime := time.Unix(1_700_000_300, 0).UTC()
	afterTime := beforeTime.Add(time.Second)
	before := providerLocaleFence{
		sourceLocale: "ko", documentRevision: uuid.NewString(), localeUpdatedAt: &beforeTime,
		exists: true,
	}
	after := providerLocaleFence{
		sourceLocale: "ko", documentRevision: uuid.NewString(), localeUpdatedAt: &afterTime,
		exists: true,
	}
	result, err := classifyAppliedProviderTranslation("ko", before, after)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.DocumentStateChanged)
	require.Equal(t, after.documentRevision, result.DocumentRevision)
	require.Empty(t, result.TargetRevision)
}

func TestClassifyAppliedProviderTranslationRejectsInvalidRoleEffects(t *testing.T) {
	t.Parallel()

	updatedAt := time.Unix(1_700_000_300, 0).UTC()
	later := updatedAt.Add(time.Second)
	documentRevision := uuid.NewString()
	for name, test := range map[string]struct {
		targetLocale string
		before       providerLocaleFence
		after        providerLocaleFence
	}{
		"source values without document revision": {
			targetLocale: "ko",
			before: providerLocaleFence{
				sourceLocale: "ko", documentRevision: documentRevision,
				localeUpdatedAt: &updatedAt, exists: true,
			},
			after: providerLocaleFence{
				sourceLocale: "ko", documentRevision: documentRevision,
				localeUpdatedAt: &later, exists: true,
			},
		},
		"target with document revision": {
			targetLocale: "ja",
			before: providerLocaleFence{
				sourceLocale: "ko", documentRevision: documentRevision,
				targetRevision: "tr1_before", localeUpdatedAt: &updatedAt, exists: true,
			},
			after: providerLocaleFence{
				sourceLocale: "ko", documentRevision: uuid.NewString(),
				targetRevision: "tr1_after", localeUpdatedAt: &later, exists: true,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := classifyAppliedProviderTranslation(test.targetLocale, test.before, test.after)
			require.Error(t, err)
		})
	}
}
