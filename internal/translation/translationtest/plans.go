package translationtest

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// Plans builds every entity's plan the way the scalar domains do: one
// plain-text unit per non-empty title, summary and body, so a fake entity
// exchanges through the real XLIFF path.
type Plans struct{}

func (Plans) BuildExtractionPlan(job application.PlanTarget, source *translation.SourceDocument) (*translation.ExtractionPlan, error) {
	if source == nil {
		return nil, translation.ErrNoTranslatableUnits
	}
	units := make([]translation.Unit, 0, 3)
	for _, field := range []struct{ name, text string }{
		{"title", source.Title}, {"summary", value(source.Summary)}, {"content_text", value(source.ContentText)},
	} {
		text := strings.TrimSpace(field.text)
		if text == "" {
			continue
		}
		units = append(units, translation.Unit{
			UnitID: "entity:" + field.name, EntityType: job.EntityType, EntityID: job.EntityID,
			Path: "entity:" + field.name, ContainerType: translation.ContainerTypeEntity, ContainerID: job.EntityID,
			FieldName: field.name, SourceText: text, SourceFormat: translation.SourceFormatPlainText,
			SourceLocale: job.SourceLocale,
		})
	}
	if len(units) == 0 {
		return nil, translation.ErrNoTranslatableUnits
	}
	title := strings.TrimSpace(source.Title)
	return &translation.ExtractionPlan{
		EntityType: job.EntityType, EntityID: job.EntityID,
		SourceLocale: job.SourceLocale, TargetLocale: job.TargetLocale,
		ContextTitle: &title,
		Units:        units,
		Bundles:      translation.BuildBundles(job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, units, source.Summary),
	}, nil
}

func value(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

var _ application.DomainPlans = Plans{}
