package application

import (
	"context"
	"fmt"
	"testing"

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
	unchanged, err := loadTranslationGenerationResources(context.Background(), nil, &Job{}, request)
	require.NoError(t, err)
	require.Empty(t, unchanged.Document.File.Groups[0].TranslationUnit[0].OriginalData)
}

// resourceStore answers the settings and site names generation resources
// read; a test reaching anything else panics on the nil JobStore.
type resourceStore struct {
	JobStore
	terms       []string
	siteTitle   string
	companyName string
	siteErr     error
}

func (s resourceStore) RuntimeSettings(context.Context) (translation.RuntimeSettings, error) {
	return translation.RuntimeSettings{DefaultLocale: "en", ProtectedTerms: s.terms}, nil
}

func (s resourceStore) SiteNames(context.Context) (string, string, error) {
	return s.siteTitle, s.companyName, s.siteErr
}

func TestGenerationResourcesReadsCurrentSiteNamesAndProtectsProviderInput(t *testing.T) {
	for _, names := range [][2]string{{"Harbor", "Harbor Company"}, {" New Site ", "New Company"}, {"Same", "Same"}, {"", ""}} {
		store := resourceStore{terms: []string{"Photoshop"}, siteTitle: names[0], companyName: names[1]}
		source := "Use " + names[0] + " twice: " + names[0] + " and " + names[1] + " with Photoshop {{site.title}} {{site.companyName}}"
		request := translation.ProviderRequest{Document: translation.XLIFFDocument{
			Version: translation.XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: translation.XLIFFFile{ID: "post:1", Groups: []translation.XLIFFGroup{{ID: "body", TranslationUnit: []translation.XLIFFUnit{{ID: "u1", Source: source}}}}},
		}}
		protected, err := loadTranslationGenerationResources(t.Context(), store, &Job{}, request)
		require.NoError(t, err)
		for _, term := range names {
			if term != "" {
				require.Contains(t, protected.Profile.ProtectedTerms, term)
			}
		}
		unit := protected.Document.File.Groups[0].TranslationUnit[0]
		require.Equal(t, source, unit.Source)
		target := translation.UnitResult{TranslatedText: source, TargetInline: unit.SourceInline, OriginalData: unit.OriginalData}
		candidate := translation.ProviderResponse{Document: translation.XLIFFWithTargets(protected.Document, map[string]translation.UnitResult{"u1": target})}
		require.True(t, translation.ValidateResponse(protected.Document, candidate, protected.Profile.ProtectedTerms...).Passed)
		body, err := translation.MarshalXLIFF(&protected.Document)
		require.NoError(t, err)
		require.Contains(t, string(body), "<ph ")
		require.Contains(t, protected.Profile.ProtectedTerms, "Photoshop")
	}
}

func TestGenerationResourcesSiteSettingsReadFailureStopsRequest(t *testing.T) {
	store := resourceStore{siteErr: fmt.Errorf("settings unavailable")}
	_, err := loadTranslationGenerationResources(t.Context(), store, &Job{}, translation.ProviderRequest{})
	require.ErrorContains(t, err, "settings unavailable")
}

func TestGenerateValidatedTranslationRejectsSiteNameCountDrift(t *testing.T) {
	for _, target := range []string{"항구 Company", "Harbor Company Harbor", "Harbor Company Harbor Company"} {
		t.Run(target, func(t *testing.T) {
			request := translationJobProviderRequest(translation.XLIFFUnit{ID: "u1", Source: "Harbor Company"})
			request.Profile.ProtectedTerms = []string{"Harbor", "Harbor Company"}
			session := &scriptedTranslationGeneratorSession{initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(request, translation.UnitResult{UnitID: "u1", TranslatedText: target})}}
			_, err := (&TranslationJobManager{}).generateValidatedTranslationWithGenerator(t.Context(), &Job{}, request, &scriptedTranslationGenerator{session: session})
			require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
			require.True(t, session.closed)
		})
	}
}
