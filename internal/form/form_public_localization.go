package form

// OverlayLocalizationRow overlays a target locale on the source locale it
// translates and reports whether any value fell back to the source: a
// missing title, schema unit, text or OG image does, an explicit empty one
// does not. The schema keeps the source topology.
func OverlayLocalizationRow(source, target LocalizationRow) (LocalizationRow, bool, error) {
	result := target
	fallback := false
	if result.Title == nil {
		result.Title = source.Title
		fallback = source.Title != nil
	}
	if result.ContentJSON == nil {
		result.ContentJSON = source.ContentJSON
		fallback = fallback || source.ContentJSON != nil
	} else if len(result.ContentJSON) > 0 && len(source.ContentJSON) > 0 {
		localizedTexts, err := formSchemaTranslationTexts(result.ContentJSON)
		if err != nil {
			return LocalizationRow{}, false, err
		}
		sourceTexts, err := formSchemaTranslationTexts(source.ContentJSON)
		if err != nil {
			return LocalizationRow{}, false, err
		}
		for unitID := range sourceTexts {
			if _, exists := localizedTexts[unitID]; !exists {
				fallback = true
				break
			}
		}
		canonical, canonicalText, err := CanonicalizeLocalizedFormSchema(source.ContentJSON, result.ContentJSON)
		if err != nil {
			return LocalizationRow{}, false, err
		}
		result.ContentJSON = canonical
		if canonicalText != nil {
			result.ContentText = canonicalText
		}
	}
	if result.ContentText == nil {
		result.ContentText = source.ContentText
		fallback = fallback || source.ContentText != nil
	}
	if result.OgAssetID == nil {
		result.OgAssetID = source.OgAssetID
		fallback = fallback || source.OgAssetID != nil
	}
	return result, fallback, nil
}
