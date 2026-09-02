package application

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

func loadTranslationGenerationResources(
	ctx context.Context,
	db *gorm.DB,
	job *model.TranslationJob,
	request translation.ProviderRequest,
) (translation.ProviderRequest, error) {
	if db == nil || job == nil {
		return request, nil
	}

	settings, err := loadTranslationRuntimeSettings(ctx, db)
	if err != nil {
		return translation.ProviderRequest{}, err
	}
	request.Profile.ProtectedTerms = translation.NormalizeProtectedTerms(append(
		request.Profile.ProtectedTerms,
		matchingTranslationProtectedTerms(request.Document, settings.ProtectedTerms)...,
	))
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
