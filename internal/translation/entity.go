package translation

import "strings"

// NewEntityUnit creates a stable entity-level translation unit.
func NewEntityUnit(entityType, entityID, sourceLocale, fieldName, sourceText string) Unit {
	return Unit{
		UnitID:        "entity:" + fieldName,
		EntityType:    entityType,
		EntityID:      entityID,
		Path:          "entity:" + fieldName,
		ContainerType: ContainerTypeEntity,
		ContainerID:   entityID,
		FieldName:     fieldName,
		SourceText:    sourceText,
		SourceFormat:  SourceFormatPlainText,
		SourceLocale:  sourceLocale,
	}
}

// ApplyCandidateFields applies entity-level translation results from bundles
// to a candidate. Structured and HTML body fields remain owned by their
// format-specific adapters.
func ApplyCandidateFields(candidate *Candidate, bundles []Bundle, results map[string]UnitResult) {
	if candidate == nil {
		return
	}
	for _, bundle := range bundles {
		for _, unit := range bundle.Units {
			if unit.ContainerType != ContainerTypeEntity {
				continue
			}
			result, ok := results[unit.UnitID]
			if !ok {
				continue
			}
			applyCandidateField(candidate, unit, result)
		}
	}
}

func applyCandidateField(candidate *Candidate, unit Unit, result UnitResult) {
	text := strings.TrimSpace(result.TranslatedText)
	switch unit.FieldName {
	case "title":
		candidate.Title = explicitStringPointer(text)
	case "summary":
		candidate.Summary = explicitStringPointer(text)
	case "content_html":
		if unit.ContainerType != ContainerTypeHTMLNode {
			assignMissingCandidateContent(candidate, text, true)
		}
	case "content_text":
		assignMissingCandidateContent(candidate, text, false)
	}
}

func assignMissingCandidateContent(candidate *Candidate, text string, includeHTML bool) {
	if includeHTML && candidate.ContentHTML == nil {
		candidate.ContentHTML = explicitStringPointer(text)
	}
	if candidate.ContentText == nil {
		candidate.ContentText = explicitStringPointer(text)
	}
}

func explicitStringPointer(value string) *string {
	copy := value
	return &copy
}

// NonBlankString returns a pointer to value, or nil when value is blank.
func NonBlankString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}
