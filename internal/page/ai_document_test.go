package page

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPageAIDocumentMetadataCreatesTargetAndPreservesExplicitEmpty(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")
	empty := ""

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", false,
		AIDocumentMetadataPatch{EnsureLocale: true, SetTitle: true, Title: &empty, SetSummary: true, Summary: &empty},
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"ko"}, effect.ChangedLocales)

	locale, exists, err := loadPageAIDocumentLocale(context.Background(), db, pageID, "ko", false)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotNil(t, locale.Title)
	require.Empty(t, *locale.Title)
	require.NotNil(t, locale.Summary)
	require.Empty(t, *locale.Summary)
}

func TestPageAIDocumentMetadataRejectsPresenceRace(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")
	empty := ""

	_, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", true,
		AIDocumentMetadataPatch{SetTitle: true, Title: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "presence changed")
}

func TestPageAIDocumentMetadataMissingTargetNoOpDoesNotCreateLocale(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", false,
		AIDocumentMetadataPatch{}, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.False(t, effect.Changed)
	_, exists, err := loadPageAIDocumentLocale(context.Background(), db, pageID, "ko", false)
	require.NoError(t, err)
	require.False(t, exists)
}

func newPageAIDocumentMetadataDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE page (
		id TEXT PRIMARY KEY,
		source_locale TEXT NOT NULL,
		document_layout TEXT NOT NULL DEFAULT '{"contentHeight":"content","pageChrome":"flow","footer":"flow"}',
		updated_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE page_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		title TEXT,
		summary TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)
	return db
}

func TestPageAIDocumentLayoutMetadataNoopAndRollback(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")
	layout := model.DefaultDocumentLayout()
	before := time.Now().UTC()
	effect, err := applyPageDocumentLayoutMetadata(t.Context(), db, pageID, layout, before, nil)
	require.NoError(t, err)
	require.False(t, effect.Changed)
	layout.ContentHeight = model.DocumentContentHeightViewport
	layout.Footer = model.DocumentRegionPlacementPinned
	effect, err = applyPageDocumentLayoutMetadata(t.Context(), db, pageID, layout, before, nil)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	rollback := errors.New("rollback following body failure")
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := applyPageDocumentLayoutMetadata(t.Context(), tx, pageID, model.DefaultDocumentLayout(), time.Now().UTC(), nil)
		if err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var loaded model.Page
	require.NoError(t, db.Where("id = ?", pageID).Take(&loaded).Error)
	require.Equal(t, layout, loaded.DocumentLayout)
	require.Equal(t, []string{"documentLayout"}, pageAIDocumentContentUpdatedFields(AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetDocumentLayout: true}}))
}

func TestPageAIDocumentLayoutMetadataRejectsNonSourceAtOwningBoundary(t *testing.T) {
	contributor, revision, document := uuid.New(), uuid.New(), uuid.New()
	service := &AIDocumentService{internal: &InternalPageService{}}
	_, _, err := service.applyAIDocumentMutationInTransaction(t.Context(), nil, AIDocumentMutation{
		PageID: uuid.NewString(), Locale: "ko", ObservedSourceLocale: "en", ExpectedRevision: revision,
		ContributorMemberID: contributor.String(),
		Metadata:            AIDocumentMetadataPatch{SetDocumentLayout: true, DocumentLayout: model.DefaultDocumentLayout()},
		Batch:               contentblock.Batch{DocumentID: document, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor}},
	}, nil)
	require.ErrorContains(t, err, "only the source locale")
}

func seedPageAIDocumentSource(t *testing.T, db *gorm.DB, pageID, locale, title string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`INSERT INTO page (id, source_locale) VALUES (?, ?)`, pageID, locale).Error)
	require.NoError(t, db.Exec(`INSERT INTO page_translation (
		entity_id, locale, title, summary, created_at, updated_at
	) VALUES (?, ?, ?, NULL, ?, ?)`, pageID, locale, title, now, now).Error)
}
