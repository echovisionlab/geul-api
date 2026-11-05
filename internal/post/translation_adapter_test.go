package post

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

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
