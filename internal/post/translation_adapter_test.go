package post

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestApplyPostMachineTranslatedMetadataCreatesValuesOnlyTarget(t *testing.T) {
	store := &stubStore{}

	title := "Translated title"
	summary := "Translated summary"
	now := time.Unix(1_700_000_000, 0).UTC()
	changed, err := applyPostTranslatedMetadata(
		t.Context(), store,
		TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &title, Summary: &summary},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []LocaleMetadata{{Locale: "ko", Title: &title, Summary: &summary, UpdatedAt: now}}, store.written)
}

func TestApplyPostMachineTranslatedMetadataReportsAnUnchangedTarget(t *testing.T) {
	title := "same"
	store := &stubStore{texts: map[string]AIDocumentLocaleMetadata{"ko": {Title: &title}}}
	now := time.Unix(1_700_000_000, 0).UTC()

	changed, err := applyPostTranslatedMetadata(
		t.Context(), store,
		TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &title},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, store.written, 1, "an unchanged delivery still stores its values")
}

func TestApplyPostMachineTranslatedMetadataPreservesExplicitEmptyTargetTitle(t *testing.T) {
	store := &stubStore{}
	now := time.Unix(1_700_000_000, 0).UTC()
	empty := ""

	changed, err := applyPostTranslatedMetadata(
		t.Context(), store,
		TranslationJob{EntityType: "post", EntityID: "post-1", TargetLocale: "ko"},
		&translation.Candidate{Title: &empty},
		translation.EntryWrite{Now: now},
		postTranslationCandidateRequireTitle,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, store.written, 1)
	require.NotNil(t, store.written[0].Title, "explicit empty target must not collapse to a missing value")
	require.Empty(t, *store.written[0].Title)
}

func TestPostTranslationCandidateValidationKeepsSparseInterchangeScoped(t *testing.T) {
	candidate := &translation.Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}

	require.Error(t, validatePostTranslationCandidate(
		candidate, postTranslationCandidateRequireTitle,
	), "AI/provider apply must still require a translated title")
	require.NoError(t, validatePostTranslationCandidate(
		candidate, postTranslationCandidateAllowSparseInterchange,
	), "XLIFF PATCH may preserve an absent target title")

	empty := ""
	candidate.Title = &empty
	require.NoError(t, validatePostTranslationCandidate(
		candidate, postTranslationCandidateRequireTitle,
	), "an explicit empty target is present, not missing")
}

func TestPostProviderTargetRoleRejectsPromotedTargetAndPreservesOtherRoles(t *testing.T) {
	tests := []struct {
		name                string
		jobSourceLocale     string
		jobTargetLocale     string
		currentSourceLocale string
		wantStale           bool
	}{
		{
			name:            "target promoted to source",
			jobSourceLocale: "en", jobTargetLocale: "ko", currentSourceLocale: "ko",
			wantStale: true,
		},
		{
			name:            "target remains a target after source switch",
			jobSourceLocale: "en", jobTargetLocale: "fr", currentSourceLocale: "ko",
		},
		{
			name:            "source role still matches",
			jobSourceLocale: "ko", jobTargetLocale: "ko", currentSourceLocale: "ko",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePostProviderSourceTarget(
				test.jobSourceLocale, test.jobTargetLocale, test.currentSourceLocale,
			)
			if test.wantStale {
				require.ErrorIs(t, err, translation.ErrSourceNoLongerCurrent)
				return
			}
			require.NoError(t, err)
		})
	}
}
