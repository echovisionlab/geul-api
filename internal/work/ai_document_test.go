package work

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// aiDocumentMetadataStore holds one Work whose source locale is en with the
// title Source, recording the source rows it stores.
func aiDocumentMetadataStore(workID string, stored *[]LocaleMetadata) stubStore {
	title := "Source"
	return stubStore{
		lockSourceLocale: func(string) (string, error) { return "en", nil },
		lockedLocaleText: func(id, locale string) (LocaleMetadata, bool, error) {
			if id != workID || locale != "en" {
				return LocaleMetadata{}, false, nil
			}
			return LocaleMetadata{Locale: "en", Title: &title}, true, nil
		},
		upsertSourceLocale: func(_, locale string, title, summary *string) error {
			*stored = append(*stored, LocaleMetadata{Locale: locale, Title: title, Summary: summary})
			return nil
		},
	}
}

func TestWorkAIDocumentSourceMetadataPreservesExplicitEmptySummary(t *testing.T) {
	workID := uuid.NewString()
	var stored []LocaleMetadata
	empty := ""
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)

	effect, err := applyWorkAIDocumentMetadata(
		t.Context(), aiDocumentMetadataStore(workID, &stored), workID, "en", true,
		AIDocumentMetadataPatch{SetSummary: true, Summary: &empty},
		now,
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.True(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"en"}, effect.ChangedLocales)
	require.Len(t, stored, 1)
	require.Equal(t, "", *stored[0].Summary)
}

func TestWorkAIDocumentIdentityRequiresExactCanonicalLocale(t *testing.T) {
	workID := uuid.NewString()
	require.NoError(t, validateWorkAIDocumentIdentity(workID, "en"))
	require.Error(t, validateWorkAIDocumentIdentity(workID, "EN"))
	require.Error(t, validateWorkAIDocumentIdentity(workID, " en"))
}

func TestWorkAIDocumentMetadataRejectsPresenceRaceAndEmptySourceTitle(t *testing.T) {
	workID := uuid.NewString()
	var stored []LocaleMetadata
	store := aiDocumentMetadataStore(workID, &stored)
	empty := ""

	_, err := applyWorkAIDocumentMetadata(
		t.Context(), store, workID, "en", true,
		AIDocumentMetadataPatch{SetTitle: true, Title: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "source title cannot be empty")

	_, err = applyWorkAIDocumentMetadata(
		t.Context(), store, workID, "ko", true,
		AIDocumentMetadataPatch{SetSummary: true, Summary: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "current source locale")

	_, err = applyWorkAIDocumentMetadata(
		t.Context(), store, workID, "en", false,
		AIDocumentMetadataPatch{SetSummary: true, Summary: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "presence changed")
	require.Empty(t, stored)
}
