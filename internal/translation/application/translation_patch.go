package application

import (
	"maps"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func mergeTranslationProviderResponse(
	req translation.ProviderRequest,
	base translation.ProviderResponse,
	patch translation.ProviderResponse,
) translation.ProviderResponse {
	mergedByUnit := translation.FlattenResponse(base)
	maps.Copy(mergedByUnit, translation.FlattenResponse(patch))
	return translation.ProviderResponse{
		Document: translation.XLIFFWithTargets(req.Document, mergedByUnit),
	}
}

func normalizeTranslationProviderResponse(
	req translation.ProviderRequest,
	response translation.ProviderResponse,
) translation.ProviderResponse {
	return mergeTranslationProviderResponse(req, translation.ProviderResponse{}, response)
}
