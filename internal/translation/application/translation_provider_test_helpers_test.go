package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/stretchr/testify/require"
)

func translationProviderRequestForTest(
	profile translation.GenerationProfile,
	groups ...translation.XLIFFGroup,
) translation.ProviderRequest {
	return translation.ProviderRequest{
		RequestID: "job-test", OperationID: "test:1", Profile: profile,
		Document: translation.XLIFFDocument{
			Version:      translation.XLIFFVersion,
			SourceLocale: profile.SourceLocale,
			TargetLocale: profile.TargetLocale,
			File:         translation.XLIFFFile{ID: "test:1", Groups: groups},
		},
	}
}

func translationProviderResponseForTest(
	request translation.ProviderRequest,
	targets ...translation.UnitResult,
) translation.ProviderResponse {
	byID := make(map[string]translation.UnitResult, len(targets))
	for _, target := range targets {
		byID[target.UnitID] = target
	}
	raw, err := translation.NewProviderRawResponse("application/json", []byte(`{"provider":"test"}`))
	if err != nil {
		panic(err)
	}
	return translation.ProviderResponse{
		Document: translation.XLIFFWithTargets(request.Document, byID),
		Raw:      raw,
	}
}

func translationProviderResponseForPlanTest(
	t *testing.T,
	plan *translation.ExtractionPlan,
	targets ...translation.UnitResult,
) *translation.ProviderResponse {
	t.Helper()
	document, err := translation.BuildXLIFFDocument(plan)
	require.NoError(t, err)
	request := translation.ProviderRequest{Document: *document}
	response := translationProviderResponseForTest(request, targets...)
	return &response
}
