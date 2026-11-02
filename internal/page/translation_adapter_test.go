package page

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPageTranslationMetadataRejectsLegacyBodyAndTargetSourceMutation(t *testing.T) {
	pageID := uuid.NewString()
	require.NoError(t, validatePageSourceLocaleMutations(
		pageID,
		"en",
		[]contentblock.LocaleMutationGroup{{Locale: "en"}},
	))
	require.NoError(t, validatePageSourceLocaleMutations(pageID, "en", nil), "structural deletion has no explicit locale writes")
	require.Error(t, validatePageSourceLocaleMutations(
		pageID,
		"en",
		[]contentblock.LocaleMutationGroup{{Locale: "ko"}},
	))

	err := upsertTranslationMetadataEntry(
		context.Background(),
		newMemoryStore(),
		pageID,
		"de",
		translation.EntryWrite{ContentJSON: []byte(`{}`)},
	)
	require.Error(t, err)
}

func TestUpsertPageTranslationMetadataPersistsOnlyLocaleValues(t *testing.T) {
	store := newMemoryStore()
	pageID := uuid.NewString()
	title := "Titre traduit"
	explicitEmpty := ""
	now := time.Unix(1_700_000_100, 0).UTC()
	require.NoError(t, upsertTranslationMetadataEntry(
		context.Background(), store, pageID, "fr",
		translation.EntryWrite{Title: &title, Summary: &explicitEmpty, Now: now},
	))
	row, _, err := store.Locale(context.Background(), pageID, "fr", Unlocked)
	require.NoError(t, err)
	require.Equal(t, title, *row.Title)
	require.NotNil(t, row.Summary)
	require.Empty(t, *row.Summary, "explicit empty is a stored target value")

	nextExplicitEmpty := ""
	require.NoError(t, upsertTranslationMetadataEntry(
		context.Background(), store, pageID, "fr",
		translation.EntryWrite{Title: &nextExplicitEmpty, Summary: nil, Now: now.Add(time.Second)},
	))
	row, _, err = store.Locale(context.Background(), pageID, "fr", Unlocked)
	require.NoError(t, err)
	require.NotNil(t, row.Title)
	require.Empty(t, *row.Title, "explicit empty must not be collapsed to missing")
	require.Nil(t, row.Summary, "nil is a missing target unit")
	require.True(t, now.Add(time.Second).Equal(row.UpdatedAt))
}
