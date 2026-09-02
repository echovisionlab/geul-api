package application

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestAITranslationGeneratorTranslatesBundlesWithTestProvider(t *testing.T) {
	t.Parallel()

	generator := translation.NewAIGenerator(newTestAITextProvider(0))
	profile := translation.GenerationProfile{
		QualityTier:    translation.QualityTierHigh,
		PreserveMarkup: false,
		SourceLocale:   "en",
		TargetLocale:   "ko",
		MIMEType:       "text/plain",
	}
	req := translationProviderRequestForTest(profile, translation.XLIFFGroup{
		ID: "main",
		TranslationUnit: []translation.XLIFFUnit{
			{ID: "title", Source: "Hello"},
			{ID: "summary", Source: "Quiet summary"},
		},
	})
	req.RequestID = "job-1"
	req.OperationID = "operation-1"

	resp, err := generator.Translate(context.Background(), req)
	require.NoError(t, err)
	targets := translation.XLIFFTargets(resp.Document)
	require.Len(t, targets, 2)
	assert.Equal(t, "[ko] Hello", targets["title"].TranslatedText)
	assert.Equal(t, "[ko] Quiet summary", targets["summary"].TranslatedText)
}

func TestAITranslationGeneratorRoundTripsFormattedSemanticInline(t *testing.T) {
	t.Parallel()
	request := translationProviderRequestForTest(translation.GenerationProfile{
		QualityTier: translation.QualityTierHigh, SourceLocale: "en", TargetLocale: "ko", MIMEType: "application/xliff+xml",
	}, translation.XLIFFGroup{ID: "body", TranslationUnit: []translation.XLIFFUnit{{
		ID: "u1", Source: "Hello world",
		OriginalData: []translation.XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}, {ID: "d3"}, {ID: "d4"}},
		SourceInline: []translation.XLIFFInline{
			{Kind: translation.XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", Children: []translation.XLIFFInline{{Kind: translation.XLIFFInlineText, Text: "Hello "}}},
			{Kind: translation.XLIFFInlinePairedCode, ID: "r2", DataRefStart: "d3", DataRefEnd: "d4", Children: []translation.XLIFFInline{{Kind: translation.XLIFFInlineText, Text: "world"}}},
		},
	}}})
	response, err := translation.NewAIGenerator(newTestAITextProvider(0)).Translate(context.Background(), request)
	require.NoError(t, err)
	result := translation.XLIFFTargets(response.Document)["u1"]
	require.Equal(t, "[ko] Hello [ko] world", result.TranslatedText)
	require.Len(t, result.TargetInline, 2)
	require.Equal(t, "r1", result.TargetInline[0].ID)
	require.Equal(t, "r2", result.TargetInline[1].ID)
}

func TestBuildTranslationProviderRequestUsesAvailableTextUnits(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{
		ID:           "job-1",
		OperationID:  "operation-1",
		EntityType:   "series",
		EntityID:     "series-1",
		SourceLocale: "en",
		TargetLocale: "ja",
	}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, &translation.SourceDocument{
		Title:       "Hello",
		Summary:     translationJobStringPtr("Summary"),
		ContentText: translationJobStringPtr("Body"),
	})
	require.NoError(t, err)

	req, err := buildTranslationProviderRequest(job, plan)
	require.NoError(t, err)

	require.Len(t, req.Document.File.Groups, 1)
	group := req.Document.File.Groups[0]
	assert.Equal(t, "entity:meta", group.ID)
	assert.Equal(t, []string{"entity:title", "entity:summary", "entity:content_text"}, []string{
		group.TranslationUnit[0].ID,
		group.TranslationUnit[1].ID,
		group.TranslationUnit[2].ID,
	})
	assert.Equal(t, "title", group.TranslationUnit[0].FieldName)
	assert.Equal(t, translation.SourceFormatPlainText, group.TranslationUnit[0].SourceFormat)
	assert.Equal(t, "entity:title", group.TranslationUnit[0].Name)
	assert.Equal(t, translation.ContainerTypeEntity, group.TranslationUnit[0].ContainerType)
	assert.Equal(t, "series-1", group.TranslationUnit[0].ContainerID)
	assert.Equal(t, translation.ContentKindEditorial, req.Profile.ContentKind)
	assert.Equal(t, translation.RegisterNeutralPlain, req.Profile.TargetRegister)
	assert.Equal(t, translation.RegisterPolicyTargetDefault, req.Profile.RegisterPolicy)
	assert.Contains(t, req.Profile.StyleInstructions, "Use neutral written Japanese plain style, da/de-aru style, and do not mix in desu/masu endings unless the content is direct user guidance.")
}

func TestBuildTranslationProviderRequestPreservesMarkupForHTMLUnits(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{
		ID:           "job-2",
		OperationID:  "operation-1",
		EntityType:   "email_layout",
		EntityID:     "layout-1",
		SourceLocale: "en",
		TargetLocale: "ko",
	}
	sourceHTML, err := email.CanonicalizeLayoutSourceMarkers(
		`<p>Hello {{name}},</p><p><a href="{{login_url}}">Log in</a></p>`,
	)
	require.NoError(t, err)
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, &translation.SourceDocument{
		ContentHTML: translationJobStringPtr(sourceHTML),
	})
	require.NoError(t, err)

	req, err := buildTranslationProviderRequest(job, plan)
	require.NoError(t, err)

	assert.True(t, req.Profile.PreserveMarkup)
	assert.Equal(t, translation.ContentKindDirectUserGuidance, req.Profile.ContentKind)
	assert.Equal(t, translation.RegisterPolite, req.Profile.TargetRegister)
	require.Len(t, req.Document.File.Groups, 1)
	require.Len(t, req.Document.File.Groups[0].TranslationUnit, 2)
	assert.Equal(t, translation.SourceFormatHTMLText, req.Document.File.Groups[0].TranslationUnit[0].SourceFormat)
}

func TestBuildTranslationProviderRequestIncludesProtectedTerms(t *testing.T) {
	t.Parallel()

	job := &model.TranslationJob{
		ID:           "job-series-1",
		OperationID:  "operation-series-1",
		EntityType:   "series",
		EntityID:     "series-1",
		SourceLocale: "en",
		TargetLocale: "ko",
	}
	plan, err := buildTranslationExtractionPlan(testTranslationDomains{}, job, &translation.SourceDocument{
		Title:          "New Artist",
		ContentText:    translationJobStringPtr("Hello! This is Garcia Martinez."),
		ProtectedTerms: []string{"New Artist", "Garcia Martinez"},
	})
	require.NoError(t, err)

	req, err := buildTranslationProviderRequest(job, plan)
	require.NoError(t, err)

	assert.Equal(t, []string{"New Artist", "Garcia Martinez"}, req.Profile.ProtectedTerms)
}

func TestValidateTranslationProviderResponseDetectsLineBreakMismatch(t *testing.T) {
	t.Parallel()

	request := translationProviderRequestForTest(
		translation.GenerationProfile{SourceLocale: "en", TargetLocale: "fr"},
		translation.XLIFFGroup{ID: "block:1", TranslationUnit: []translation.XLIFFUnit{{
			ID: "block:1:text:0", Source: "Line one\nLine two",
		}}},
	)
	response := translationProviderResponseForTest(request, translation.UnitResult{
		UnitID: "block:1:text:0", TranslatedText: "Ligne un Ligne deux",
	})
	result := translation.ValidateResponse(request.Document, response)

	require.False(t, result.Passed)
	require.Equal(t, []string{"block:1:text:0"}, result.LineBreakMismatchUnitIDs)
}
