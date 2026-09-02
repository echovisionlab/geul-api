package application

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchingTranslationProtectedTermsIsExactCaseSensitiveAndSourceBounded(t *testing.T) {
	t.Parallel()
	document := translation.XLIFFDocument{File: translation.XLIFFFile{Groups: []translation.XLIFFGroup{{
		TranslationUnit: []translation.XLIFFUnit{{Source: "Photoshop and React Native, not react native."}},
	}}}}

	matched := matchingTranslationProtectedTerms(document, []string{
		"Photoshop", "React Native", "react native", "Missing",
	})

	assert.Equal(t, []string{"Photoshop", "React Native", "react native"}, matched)
}

func TestLoadTranslationGenerationResourcesDefersProtectionUntilTermsCanBeMerged(t *testing.T) {
	t.Parallel()
	document := translation.XLIFFDocument{File: translation.XLIFFFile{Groups: []translation.XLIFFGroup{{
		TranslationUnit: []translation.XLIFFUnit{{Source: "Use React Native in Photoshop"}},
	}}}}
	request := translation.ProviderRequest{
		Profile:  translation.GenerationProfile{ProtectedTerms: []string{"React Native"}},
		Document: document,
	}

	// A nil DB intentionally exercises the pure request boundary: protection is
	// deferred until domain and settings terms have been merged by the owning
	// generation-resource loader.
	unchanged, err := loadTranslationGenerationResources(context.Background(), nil, &model.TranslationJob{}, request)
	require.NoError(t, err)
	require.Empty(t, unchanged.Document.File.Groups[0].TranslationUnit[0].OriginalData)
}
