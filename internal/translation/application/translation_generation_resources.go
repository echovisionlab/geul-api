package application

import (
	"context"
	"slices"
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
	var siteSettings model.SiteSettings
	if err := db.WithContext(ctx).Select("site_title", "company_name").Where("id = 1").Take(&siteSettings).Error; err != nil {
		return translation.ProviderRequest{}, err
	}
	request.Profile.ProtectedTerms = translation.NormalizeProtectedTerms(append(
		request.Profile.ProtectedTerms,
		matchingTranslationProtectedTerms(request.Document, settings.ProtectedTerms)...,
	))
	// Settings values are literal spellings; retain even their edge whitespace.
	for _, term := range matchingTranslationProtectedTerms(request.Document, []string{siteSettings.SiteTitle, siteSettings.CompanyName}) {
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
