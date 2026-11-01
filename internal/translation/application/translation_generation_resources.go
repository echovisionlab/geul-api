package application

import (
	"context"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func loadTranslationGenerationResources(
	ctx context.Context,
	store JobStore,
	job *Job,
	request translation.ProviderRequest,
) (translation.ProviderRequest, error) {
	if store == nil || job == nil {
		return request, nil
	}

	settings, err := store.RuntimeSettings(ctx)
	if err != nil {
		return translation.ProviderRequest{}, err
	}
	siteTitle, companyName, err := store.SiteNames(ctx)
	if err != nil {
		return translation.ProviderRequest{}, err
	}
	request.Profile.ProtectedTerms = translation.NormalizeProtectedTerms(append(
		request.Profile.ProtectedTerms,
		matchingTranslationProtectedTerms(request.Document, settings.ProtectedTerms)...,
	))
	// Settings values are literal spellings; retain even their edge whitespace.
	for _, term := range matchingTranslationProtectedTerms(request.Document, []string{siteTitle, companyName}) {
		if strings.TrimSpace(term) != "" && !slices.Contains(request.Profile.ProtectedTerms, term) {
			request.Profile.ProtectedTerms = append(request.Profile.ProtectedTerms, term)
		}
	}
	if err := translation.ProtectXLIFFTerms(&request.Document, request.Profile.ProtectedTerms); err != nil {
		return translation.ProviderRequest{}, err
	}
	return request, nil
}

func matchingTranslationProtectedTerms(
	document translation.XLIFFDocument,
	terms []string,
) []string {
	if len(terms) == 0 {
		return nil
	}

	sourceTexts := make([]string, 0)
	for _, group := range document.File.Groups {
		for _, unit := range group.TranslationUnit {
			sourceTexts = append(sourceTexts, unit.Source)
		}
	}

	matched := make([]string, 0, len(terms))
	for _, term := range terms {
		for _, sourceText := range sourceTexts {
			if strings.Contains(sourceText, term) {
				matched = append(matched, term)
				break
			}
		}
	}
	return matched
}
