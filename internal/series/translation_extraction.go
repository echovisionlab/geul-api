package series

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// BuildTranslationExtractionPlan maps the Series-owned title, summary, and
// text fallback fields into the provider-neutral Translation contract.
func BuildTranslationExtractionPlan(
	seriesID string,
	sourceLocale string,
	targetLocale string,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}

	units := make([]translation.Unit, 0, 3)
	appendUnit := func(fieldName string, sourceText string) {
		sourceText = strings.TrimSpace(sourceText)
		if sourceText == "" {
			return
		}
		units = append(units, translation.Unit{
			UnitID:        "entity:" + fieldName,
			EntityType:    "series",
			EntityID:      seriesID,
			Path:          "entity:" + fieldName,
			ContainerType: translation.ContainerTypeEntity,
			ContainerID:   seriesID,
			FieldName:     fieldName,
			SourceText:    sourceText,
			SourceFormat:  translation.SourceFormatPlainText,
			SourceLocale:  sourceLocale,
		})
	}

	appendUnit("title", source.Title)
	appendUnit("summary", derefString(source.Summary))
	appendUnit("content_text", derefString(source.ContentText))
	if len(units) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}

	contextTitle := trimmedStringPointer(source.Title)
	contextText := trimmedStringPointer(derefString(source.Summary))
	return &translation.ExtractionPlan{
		EntityType:     "series",
		EntityID:       seriesID,
		SourceLocale:   sourceLocale,
		TargetLocale:   targetLocale,
		ContextTitle:   contextTitle,
		ProtectedTerms: translation.NormalizeProtectedTerms(source.ProtectedTerms),
		Units:          units,
		Bundles: translation.BuildBundles(
			"series", seriesID, sourceLocale, targetLocale, units, contextText,
		),
	}, nil
}

func trimmedStringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
