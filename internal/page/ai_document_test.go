package page

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageAIDocumentMetadataCreatesTargetAndPreservesExplicitEmpty(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	store.putPage(pageID, "en", "Source", uuid.New())
	empty := ""

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), store, pageID, "ko", false,
		AIDocumentMetadataPatch{EnsureLocale: true, SetTitle: true, Title: &empty, SetSummary: true, Summary: &empty},
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"ko"}, effect.ChangedLocales)

	locale, exists, err := store.Locale(context.Background(), pageID, "ko", Unlocked)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotNil(t, locale.Title)
	require.Empty(t, *locale.Title)
	require.NotNil(t, locale.Summary)
	require.Empty(t, *locale.Summary)
}

func TestPageAIDocumentMetadataRejectsPresenceRace(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	store.putPage(pageID, "en", "Source", uuid.New())
	empty := ""

	_, err := applyPageAIDocumentMetadata(
		context.Background(), store, pageID, "ko", true,
		AIDocumentMetadataPatch{SetTitle: true, Title: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "presence changed")
}

func TestPageAIDocumentMetadataMissingTargetNoOpDoesNotCreateLocale(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	store.putPage(pageID, "en", "Source", uuid.New())

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), store, pageID, "ko", false,
		AIDocumentMetadataPatch{}, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.False(t, effect.Changed)
	_, exists, err := store.Locale(context.Background(), pageID, "ko", Unlocked)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestPageAIDocumentLayoutMetadataSkipsAnUnchangedLayout(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	store.putPage(pageID, "en", "Source", uuid.New())
	layout := DefaultDocumentLayout()
	effect, err := applyPageDocumentLayoutMetadata(t.Context(), store, pageID, layout, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, effect.Changed)
	layout.ContentHeight = ContentHeightViewport
	layout.Footer = PlacementPinned
	effect, err = applyPageDocumentLayoutMetadata(t.Context(), store, pageID, layout, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	stored, err := store.DocumentLayout(t.Context(), pageID)
	require.NoError(t, err)
	require.Equal(t, layout, stored)
	require.Equal(t, []string{"documentLayout"}, pageAIDocumentContentUpdatedFields(AIDocumentMutation{Metadata: AIDocumentMetadataPatch{SetDocumentLayout: true}}))
}

func TestPageAIDocumentLayoutMetadataRejectsNonSourceAtOwningBoundary(t *testing.T) {
	contributor, revision, document := uuid.New(), uuid.New(), uuid.New()
	_, err := applyAIDocumentMutationInTransaction(t.Context(), nil, AIDocumentMutation{
		PageID: uuid.NewString(), Locale: "ko", ObservedSourceLocale: "en", ExpectedRevision: revision,
		ContributorMemberID: contributor.String(),
		Metadata:            AIDocumentMetadataPatch{SetDocumentLayout: true, DocumentLayout: DefaultDocumentLayout()},
		Batch:               contentblock.Batch{DocumentID: document, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor}},
	}, nil)
	require.ErrorContains(t, err, "only the source locale")
}
