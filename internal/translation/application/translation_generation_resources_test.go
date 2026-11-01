package application

import (
	"context"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

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

func TestGenerationResourcesReadsCurrentSiteNamesAndProtectsProviderInput(t *testing.T) {
	service, mock := newTranslationEntryQueryTestService(t)
	for _, names := range [][2]string{{"Harbor", "Harbor Company"}, {" New Site ", "New Company"}, {"Same", "Same"}, {"", ""}} {
		mock.ExpectExec("INSERT INTO translation_settings").WithArgs(1, "en", "{}").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT \* FROM "translation_settings"`).WillReturnRows(sqlmock.NewRows([]string{"id", "default_locale", "protected_terms"}).AddRow(1, "en", "{Photoshop}"))
		mock.ExpectQuery(`SELECT "site_title","company_name" FROM "site_settings" WHERE id = 1`).WillReturnRows(sqlmock.NewRows([]string{"site_title", "company_name"}).AddRow(names[0], names[1]))
		source := "Use " + names[0] + " twice: " + names[0] + " and " + names[1] + " with Photoshop {{site.title}} {{site.companyName}}"
		request := translation.ProviderRequest{Document: translation.XLIFFDocument{
			Version: translation.XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: translation.XLIFFFile{ID: "post:1", Groups: []translation.XLIFFGroup{{ID: "body", TranslationUnit: []translation.XLIFFUnit{{ID: "u1", Source: source}}}}},
		}}
		protected, err := loadTranslationGenerationResources(t.Context(), service.db, &model.TranslationJob{}, request)
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
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGenerationResourcesSiteSettingsReadFailureStopsRequest(t *testing.T) {
	service, mock := newTranslationEntryQueryTestService(t)
	mock.ExpectExec("INSERT INTO translation_settings").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT \* FROM "translation_settings"`).WillReturnRows(sqlmock.NewRows([]string{"id", "default_locale", "protected_terms"}).AddRow(1, "en", "{}"))
	mock.ExpectQuery(`SELECT "site_title","company_name" FROM "site_settings"`).WillReturnError(fmt.Errorf("settings unavailable"))
	_, err := loadTranslationGenerationResources(t.Context(), service.db, &model.TranslationJob{}, translation.ProviderRequest{})
	require.ErrorContains(t, err, "settings unavailable")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGenerateValidatedTranslationRejectsSiteNameCountDrift(t *testing.T) {
	for _, target := range []string{"항구 Company", "Harbor Company Harbor", "Harbor Company Harbor Company"} {
		t.Run(target, func(t *testing.T) {
			request := translationJobProviderRequest(translation.XLIFFUnit{ID: "u1", Source: "Harbor Company"})
			request.Profile.ProtectedTerms = []string{"Harbor", "Harbor Company"}
			session := &scriptedTranslationGeneratorSession{initialResponses: []*translation.ProviderResponse{translationJobProviderResponse(request, translation.UnitResult{UnitID: "u1", TranslatedText: target})}}
			_, err := (&TranslationJobManager{}).generateValidatedTranslationWithGenerator(t.Context(), &model.TranslationJob{}, request, &scriptedTranslationGenerator{session: session})
			require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
			require.True(t, session.closed)
		})
	}
}
