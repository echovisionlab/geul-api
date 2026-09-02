package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerPatchRequestForTest() translation.ProviderRequest {
	return translationProviderRequestForTest(
		translation.GenerationProfile{SourceLocale: "en", TargetLocale: "ko"},
		translation.XLIFFGroup{ID: "entity:main", TranslationUnit: []translation.XLIFFUnit{
			{ID: "entity:title", Source: "Hello"},
			{ID: "block:1:text:0", Source: "Stays inline in work"},
		}},
	)
}

func TestMergeTranslationProviderResponseOverwritesOnlyPatchedUnits(t *testing.T) {
	t.Parallel()
	request := providerPatchRequestForTest()
	base := translationProviderResponseForTest(request,
		translation.UnitResult{UnitID: "entity:title", TranslatedText: "[ko] Hello"},
		translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[ko] stays inline in work"},
	)
	patch := translationProviderResponseForTest(request,
		translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "작품 안에서 인라인 상태를 유지합니다"},
	)

	merged := mergeTranslationProviderResponse(request, base, patch)
	targets := translation.XLIFFTargets(merged.Document)
	require.Len(t, targets, 2)
	assert.Equal(t, "[ko] Hello", targets["entity:title"].TranslatedText)
	assert.Equal(t, "작품 안에서 인라인 상태를 유지합니다", targets["block:1:text:0"].TranslatedText)
}

func TestNormalizeTranslationProviderResponseDropsUnknownUnits(t *testing.T) {
	t.Parallel()
	request := providerPatchRequestForTest()
	response := translationProviderResponseForTest(request,
		translation.UnitResult{UnitID: "entity:title", TranslatedText: "[ko] Hello"},
		translation.UnitResult{UnitID: "block:1:text:o", TranslatedText: "[ko] wrong id"},
		translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "[ko] right id"},
	)

	normalized := normalizeTranslationProviderResponse(request, response)
	targets := translation.XLIFFTargets(normalized.Document)
	require.Len(t, targets, 2)
	assert.Equal(t, "[ko] Hello", targets["entity:title"].TranslatedText)
	assert.Equal(t, "[ko] right id", targets["block:1:text:0"].TranslatedText)
}

func TestSelectTranslationProviderResponseKeepsOnlyScopedUnits(t *testing.T) {
	t.Parallel()
	request := translationProviderRequestForTest(
		translation.GenerationProfile{SourceLocale: "en", TargetLocale: "fr"},
		translation.XLIFFGroup{ID: "block:1", TranslationUnit: []translation.XLIFFUnit{
			{ID: "block:1:text:0", Source: "Hello"},
		}},
	)
	response := translationProviderResponseForTest(request,
		translation.UnitResult{UnitID: "block:1:text:0", TranslatedText: "Bonjour"},
		translation.UnitResult{UnitID: "block:2:text:0", TranslatedText: "Ignore"},
	)

	selected := translation.SelectResponse(request, response)
	targets := translation.XLIFFTargets(selected.Document)
	require.Len(t, targets, 1)
	assert.Equal(t, "Bonjour", targets["block:1:text:0"].TranslatedText)
}
