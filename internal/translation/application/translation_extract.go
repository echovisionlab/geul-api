package application

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func buildTranslationExtractionPlan(
	domains DomainRegistry,
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil {
		return nil, fmt.Errorf("translation job is required")
	}
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}

	if domains == nil {
		return nil, fmt.Errorf("translation domain registry is required")
	}
	return domains.BuildExtractionPlan(job, source)
}

func hasStructuredContentUnits(units []translation.Unit) bool {
	for _, unit := range units {
		if unit.FieldName == "content_json" {
			return true
		}
	}
	return false
}

func hasHTMLContentUnits(units []translation.Unit) bool {
	for _, unit := range units {
		if unit.FieldName == "content_html" && unit.ContainerType == translation.ContainerTypeHTMLNode {
			return true
		}
	}
	return false
}
